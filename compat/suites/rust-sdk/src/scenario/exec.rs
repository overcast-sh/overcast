//! Running a generated group (compat/model/README.md § The scenario file), and
//! the closed assertion set evaluated over the response document.
//!
//! A group is setup → tests → teardown. Setup runs every step in order and a
//! failure reports every test in the group as skip with
//! `setup failed: <the six fields>` — which the harness does for us, from the
//! error returned here. Teardown runs afterwards **even when setup failed**,
//! with every step wrapped individually: a setup that failed on its third step
//! has already created what its first two made, and no test will run to remove
//! it.

// A Failure is eleven owned strings, so clippy calls every Result carrying one
// a large Err. Boxing it would put an allocation on the path every clause
// takes, to save copying a struct that is only ever built once per failure —
// and a failure ends the step it is raised in.
#![allow(clippy::result_large_err)]

use std::future::Future;
use std::pin::Pin;
use std::time::Duration;

use serde_json::Value as Json;

use super::capture::{Document, Outcome, SdkFailure, Wire};
use super::errors;
use super::failure::{self, Failure};
use super::json;
use super::value::{clock_millis, Bag, EvalError};
use super::{Call, Check, CheckKind, Clause, Group, Test, WhereEntry};
use crate::harness::TestContext;

impl Group {
    /// Runs one generated test: the primary call, then every clause in order.
    pub async fn run_test(&self, ctx: &TestContext, test: &str, spec: Test) -> Result<(), String> {
        let e = Execution {
            group: self,
            ctx,
            test: test.to_string(),
        };
        match e.test(&spec).await {
            Ok(()) => Ok(()),
            Err(failure) => Err(failure.into_error()),
        }
    }

    /// Runs a group's setup steps in order, stopping at the first failure.
    ///
    /// The failure is returned to the harness, which reports every test in the
    /// group as skip with `setup failed: <message>` and still runs teardown. An
    /// empty list is a no-op, not a missing phase: a probe group has nothing to
    /// set up and still registers the hook, so "a probe creates nothing" is
    /// visible in the emitted source rather than being a convention to
    /// remember.
    pub async fn run_setup(&self, ctx: &TestContext, calls: Vec<Call>) -> Result<(), String> {
        let e = Execution {
            group: self,
            ctx,
            test: "setup".to_string(),
        };
        for (i, call) in calls.iter().enumerate() {
            if let Err(failure) = e.invoke(call, &format!("setup[{i}]")).await {
                // Untagged: the harness folds this into a skip reason, and a
                // classification tag inside one would be read by a person.
                return Err(failure.message());
            }
        }
        Ok(())
    }

    /// Runs a group's teardown steps, each wrapped individually: a failure skips
    /// that step and the rest still run. Each skip is logged to stderr and none
    /// of them fails the group.
    ///
    /// Returning an error instead would report a teardown failure on every clean
    /// run of a lifecycle group: the delete test has already removed the
    /// resource the teardown step names, so a "not found" there is the expected
    /// outcome, not a leak. Proof that nothing leaked is the orphan sweep — a
    /// `{run_id}` search after the run — not the teardown's own exit status.
    pub async fn run_teardown(&self, ctx: &TestContext, calls: Vec<Call>) -> Result<(), String> {
        let e = Execution {
            group: self,
            ctx,
            test: "teardown".to_string(),
        };
        for (i, call) in calls.iter().enumerate() {
            let step = format!("teardown[{i}]");
            if let Err(failure) = e.invoke(call, &step).await {
                ctx.log(&format!(
                    "{}: skipped {step}: {}",
                    self.name,
                    failure.message()
                ));
            }
        }
        Ok(())
    }
}

/// One group-scoped run of one test, setup or teardown.
struct Execution<'a> {
    group: &'a Group,
    ctx: &'a TestContext,
    /// Failure-message field 1's second half: the test name, or
    /// `setup`/`teardown` for a group hook.
    test: String,
}

/// A response together with the call that produced it, so a clause that reads
/// the primary response and a clause that makes its own call both name the
/// right operation and the right params in fields 2 and 3.
struct Observed {
    op: &'static str,
    params: String,
    /// The response document, with the wire it came off. `None` when no call
    /// succeeded — the primary call of a test that expects an error.
    body: Option<Document>,
    error: Option<SdkFailure>,
}

impl Execution<'_> {
    fn bag(&self) -> Bag<'_> {
        Bag::new(self.ctx, self.group.name)
    }

    async fn test(&self, spec: &Test) -> Result<(), Failure> {
        let want_error = spec.assert.iter().find_map(|clause| match clause {
            Clause::ErrorCode { error } => Some(error),
            _ => None,
        });

        let observed = self.invoke_raw(&spec.call, "call").await?;
        match (&want_error, &observed.error) {
            // A test carrying an errorCode clause expects its primary call to
            // fail; the generator refuses such a test any clause that would read
            // the primary response, so every other clause makes a call of its
            // own.
            (Some(want), None) => {
                return Err(self.fail(
                    &observed,
                    "call",
                    "errorCode",
                    "",
                    &errors::accepted_codes(want),
                    "<no error>",
                ))
            }
            (Some(want), Some(error)) => {
                if !errors::matches(&error.codes, want) {
                    return Err(self.fail(
                        &observed,
                        "call",
                        "errorCode",
                        "",
                        &errors::accepted_codes(want),
                        &failure::quote(&error.display),
                    ));
                }
            }
            (None, Some(_)) => return Err(self.failed_call(&observed, "call")),
            (None, None) => self.apply_exports(&spec.call, &observed, "call")?,
        }

        for (i, clause) in spec.assert.iter().enumerate() {
            if matches!(clause, Clause::ErrorCode { .. }) {
                continue; // already checked against the primary call
            }
            self.assert(clause, &observed, &format!("assert[{i}]"))
                .await?;
        }
        Ok(())
    }

    /// Builds a call's typed input and sends it, keeping the SDK's own failure
    /// on the observation rather than raising it: two clauses have to inspect
    /// it.
    ///
    /// The returned observation carries the exact params JSON sent, so every
    /// failure downstream of it quotes what went on the wire.
    async fn invoke_raw(&self, call: &Call, step: &str) -> Result<Observed, Failure> {
        // The clock is read here, once per call, so every `$now` in these
        // params sees the same instant (compat/model/README.md § Values).
        let evaluated = match call.params.eval(&self.bag().at(clock_millis())) {
            Ok(evaluated) => evaluated,
            Err(err) => {
                // Nothing was sent, so field 3 shows the params as the scenario
                // file writes them rather than a half-built input that never
                // existed.
                let raw = json::render(&call.params.raw());
                return Err(match err {
                    EvalError::Unresolved(path) => self.fail_with(
                        call.op,
                        &raw,
                        step,
                        "params",
                        &path,
                        "the context path to be set",
                        "<unset>",
                        false,
                    ),
                    EvalError::Message(_) => self.fail_with(
                        call.op,
                        &raw,
                        step,
                        "params",
                        "",
                        "a value the scenario can evaluate",
                        &failure::quote(&err.message()),
                        false,
                    ),
                });
            }
        };
        let params = json::render(&evaluated);
        let outcome = match (call.invoke)(super::Binder::new(evaluated)).await {
            Ok(outcome) => outcome,
            Err(bind) => {
                return Err(self.fail_with(
                    call.op,
                    &params,
                    step,
                    "params",
                    &bind.member,
                    "a value the input member accepts",
                    &failure::quote(&bind.message),
                    false,
                ))
            }
        };
        let Outcome { body, error } = outcome;
        Ok(Observed {
            op: call.op,
            params,
            body,
            error,
        })
    }

    /// `invoke_raw` with the SDK's failure turned into a failure — what every
    /// clause that simply needs the call to succeed wants.
    async fn call(&self, call: &Call, step: &str) -> Result<Observed, Failure> {
        let observed = self.invoke_raw(call, step).await?;
        if observed.error.is_some() {
            return Err(self.failed_call(&observed, step));
        }
        Ok(observed)
    }

    /// `call` plus its exports, for a setup or teardown step, whose whole
    /// purpose is the context values it leaves behind.
    async fn invoke(&self, call: &Call, step: &str) -> Result<Observed, Failure> {
        let observed = self.call(call, step).await?;
        self.apply_exports(call, &observed, step)?;
        Ok(observed)
    }

    /// Writes a call's response paths into the context bag.
    ///
    /// An export path that does not resolve is an error for the step that
    /// carries it: the value a later step will reference is not there, and
    /// failing here names the path instead of failing later with an
    /// unresolvable reference.
    fn apply_exports(&self, call: &Call, observed: &Observed, step: &str) -> Result<(), Failure> {
        let bag = self.bag();
        let body = observed
            .body
            .as_ref()
            .map_or(&Json::Null, |document| &document.value);
        let mut exports: Vec<&(&str, &str)> = call.export.iter().collect();
        exports.sort_by_key(|(path, _)| *path);
        for (path, response_path) in exports {
            match json::resolve(body, response_path) {
                Err(err) => {
                    return Err(self.fail(
                        observed,
                        step,
                        "export",
                        response_path,
                        "a well-formed response path",
                        &failure::quote(&err),
                    ))
                }
                Ok(None) => {
                    return Err(self.fail(
                        observed,
                        step,
                        "export",
                        response_path,
                        &format!("a value to export into {path:?}"),
                        json::MISSING,
                    ))
                }
                Ok(Some(value)) => bag.set(path, value),
            }
        }
        Ok(())
    }

    /// Evaluates one clause. `primary` is the test's own response, which
    /// `responseField` and a call-less `listContains`/`absent` read.
    fn assert<'a>(
        &'a self,
        clause: &'a Clause,
        primary: &'a Observed,
        step: &'a str,
    ) -> Pin<Box<dyn Future<Output = Result<(), Failure>> + Send + 'a>> {
        Box::pin(async move {
            match clause {
                Clause::ResponseField { checks } => {
                    self.check_all(primary, checks, "responseField", step)
                }
                Clause::Readback { call, checks } => {
                    let observed = self.call(call, step).await?;
                    self.check_all(&observed, checks, "readback", step)?;
                    // A clause's exports are applied only once the clause
                    // holds: inside an eventually, the failing attempts must not
                    // leave a half-read response in the context bag for the next
                    // clause to reference.
                    self.apply_exports(call, &observed, step)
                }
                Clause::AbsentByError { call, error } => {
                    let observed = self.invoke_raw(call, step).await?;
                    match &observed.error {
                        None => Err(self.fail(
                            &observed,
                            step,
                            "absent",
                            "",
                            &errors::accepted_codes(error),
                            "<no error>",
                        )),
                        Some(sdk) if !errors::matches(&sdk.codes, error) => Err(self.fail(
                            &observed,
                            step,
                            "absent",
                            "",
                            &errors::accepted_codes(error),
                            &failure::quote(&sdk.display),
                        )),
                        Some(_) => Ok(()),
                    }
                }
                Clause::ListContains {
                    call,
                    items_path,
                    criteria,
                }
                | Clause::AbsentFromList {
                    call,
                    items_path,
                    criteria,
                } => {
                    self.assert_list(
                        clause.kind(),
                        clause,
                        call,
                        items_path,
                        criteria,
                        primary,
                        step,
                    )
                    .await
                }
                Clause::Eventually {
                    max_attempts,
                    delay_ms,
                    assert,
                } => {
                    self.eventually(*max_attempts, *delay_ms, assert, primary, step)
                        .await
                }
                Clause::ErrorCode { .. } => Err(self.fail(
                    primary,
                    step,
                    "errorCode",
                    "",
                    "an errorCode clause on the test's own call",
                    "a nested one",
                )),
            }
        })
    }

    /// Evaluates `listContains` and the list form of `absent`.
    #[allow(clippy::too_many_arguments)]
    async fn assert_list(
        &self,
        kind: &str,
        clause: &Clause,
        call: &Option<Call>,
        items_path: &str,
        criteria: &[WhereEntry],
        primary: &Observed,
        step: &str,
    ) -> Result<(), Failure> {
        let wants_member = matches!(clause, Clause::ListContains { .. });

        // The list forms read the clause's own call when it has one, else the
        // test's own response.
        let own;
        let observed = match call {
            Some(call) => {
                own = self.call(call, step).await?;
                &own
            }
            None => primary,
        };
        let Some(document) = observed.body.as_ref() else {
            return Err(self.fail(
                observed,
                step,
                kind,
                items_path,
                "a response to read the list from",
                "<no response>",
            ));
        };

        let resolved = match json::resolve(&document.value, items_path) {
            Ok(resolved) => resolved,
            Err(err) => {
                return Err(self.fail(
                    observed,
                    step,
                    kind,
                    items_path,
                    "a well-formed items path",
                    &failure::quote(&err),
                ))
            }
        };
        // A missing list counts as empty: several AWS services omit an empty
        // list member rather than serializing [].
        let items: Vec<Json> = match resolved {
            None => Vec::new(),
            Some(Json::Array(items)) => items.clone(),
            Some(other) => {
                return Err(self.fail(
                    observed,
                    step,
                    kind,
                    items_path,
                    "a list",
                    &json::render(other),
                ))
            }
        };

        let (matched, wanted) =
            self.match_item(observed, &items, criteria, kind, step, document.wire)?;
        if wants_member {
            if matched.is_none() {
                return Err(self.fail(
                    observed,
                    step,
                    kind,
                    items_path,
                    &format!(
                        "an item matching {}",
                        failure::render_where(criteria, &wanted)
                    ),
                    &failure::render_list(&items),
                ));
            }
        } else if let Some(i) = matched {
            return Err(self.fail(
                observed,
                step,
                kind,
                items_path,
                &format!(
                    "no item matching {}",
                    failure::render_where(criteria, &wanted)
                ),
                &json::render(&items[i]),
            ));
        }
        // The clause held. A list clause may carry a call with exports of its
        // own, and they are applied on the same terms as a read-back's: only
        // once the clause holds.
        match call {
            Some(call) => self.apply_exports(call, observed, step),
            None => Ok(()),
        }
    }

    /// The index of the first item satisfying every criterion, together with the
    /// evaluated expected values so a failure message can print them.
    #[allow(clippy::too_many_arguments)]
    fn match_item(
        &self,
        observed: &Observed,
        items: &[Json],
        criteria: &[WhereEntry],
        kind: &str,
        step: &str,
        wire: Wire,
    ) -> Result<(Option<usize>, Vec<Json>), Failure> {
        let bag = self.bag();
        let mut wanted = Vec::with_capacity(criteria.len());
        for entry in criteria {
            match entry.value.eval(&bag) {
                Ok(value) => wanted.push(value),
                Err(err) => {
                    return Err(self.fail(
                        observed,
                        step,
                        kind,
                        entry.path,
                        "the where value to evaluate",
                        &failure::quote(&err.message()),
                    ))
                }
            }
        }

        for (i, item) in items.iter().enumerate() {
            let mut all = true;
            for (entry, want) in criteria.iter().zip(&wanted) {
                // "$" is the item itself, which is how a list of strings is
                // matched: where_entry("$", context("queue.url")).
                let got = match json::resolve(item, entry.path) {
                    Ok(got) => got,
                    Err(err) => {
                        return Err(self.fail(
                            observed,
                            step,
                            kind,
                            entry.path,
                            "a well-formed where path",
                            &failure::quote(&err),
                        ))
                    }
                };
                if !got.is_some_and(|got| json::equal(got, want, wire)) {
                    all = false;
                    break;
                }
            }
            if all {
                return Ok((Some(i), wanted));
            }
        }
        Ok((None, wanted))
    }

    /// Retries the inner clause up to `max_attempts` times, waiting `delay_ms`
    /// between attempts and no longer.
    ///
    /// The last failure is the reported one, behind the budget that was spent on
    /// it. Bare, it is indistinguishable from a clause evaluated once, and the
    /// two want opposite fixes: a real disagreement, or a poll budget too short
    /// for how long this service takes to settle. All the backends word the
    /// prefix identically, so a generated group's give-up reads the same
    /// whichever suite reports it.
    async fn eventually(
        &self,
        max_attempts: u32,
        delay_ms: u64,
        inner: &Clause,
        primary: &Observed,
        step: &str,
    ) -> Result<(), Failure> {
        let attempts = max_attempts.max(1);
        let inner_step = format!("{step}.assert");
        let mut last: Option<Failure> = None;
        for attempt in 0..attempts {
            if attempt > 0 && delay_ms > 0 {
                tokio::time::sleep(Duration::from_millis(delay_ms)).await;
            }
            match self.assert(inner, primary, &inner_step).await {
                Ok(()) => return Ok(()),
                Err(failure) => last = Some(failure),
            }
        }
        let mut failure = last.expect("at least one attempt was made");
        failure.prefix = format!(
            "eventually gave up after {attempts} attempt(s) {delay_ms}ms apart; last failure: "
        );
        Err(failure)
    }

    /// Evaluates every check of a clause against one response, in the order the
    /// emitter wrote them — which is path order, so a failure message is the
    /// same on every run.
    fn check_all(
        &self,
        observed: &Observed,
        checks: &[Check],
        kind: &str,
        step: &str,
    ) -> Result<(), Failure> {
        let Some(document) = observed.body.as_ref() else {
            return Err(self.fail(
                observed,
                step,
                kind,
                "",
                "a response to check",
                "<no response>",
            ));
        };
        for check in checks {
            self.check(observed, document, check, kind, step)?;
        }
        Ok(())
    }

    /// Evaluates one check against one response path.
    fn check(
        &self,
        observed: &Observed,
        document: &Document,
        check: &Check,
        kind: &str,
        step: &str,
    ) -> Result<(), Failure> {
        let full_kind = format!("{kind} {}", check.kind.as_str());
        let resolved = match json::resolve(&document.value, check.path) {
            Ok(resolved) => resolved,
            Err(err) => {
                return Err(self.fail(
                    observed,
                    step,
                    &full_kind,
                    check.path,
                    "a well-formed path",
                    &failure::quote(&err),
                ))
            }
        };
        let mismatch = |expected: &str| {
            self.fail(
                observed,
                step,
                &full_kind,
                check.path,
                expected,
                &json::render_resolved(resolved),
            )
        };

        match check.kind {
            CheckKind::Missing => {
                if resolved.is_some() {
                    return Err(mismatch("the path not to resolve"));
                }
            }
            CheckKind::IsList => {
                // True of a present list, empty or not, and of an absent
                // member: several AWS services omit an empty list rather than
                // serializing []. A present value that is not a list still
                // fails.
                if let Some(value) = resolved {
                    if !value.is_array() {
                        return Err(mismatch("a list, or no such member"));
                    }
                }
            }
            CheckKind::NonEmpty => {
                if resolved.is_none_or(json::is_empty) {
                    return Err(mismatch("a non-empty value"));
                }
            }
            CheckKind::Equals => {
                let want = match check.value.as_ref().map(|value| value.eval(&self.bag())) {
                    Some(Ok(want)) => want,
                    Some(Err(err)) => {
                        return Err(self.fail(
                            observed,
                            step,
                            &full_kind,
                            check.path,
                            "the expected value to evaluate",
                            &failure::quote(&err.message()),
                        ))
                    }
                    None => Json::Null,
                };
                if !resolved.is_some_and(|got| json::equal(got, &want, document.wire)) {
                    return Err(mismatch(&json::render(&want)));
                }
            }
            CheckKind::Matches => {
                let pattern = match check.value.as_ref() {
                    Some(super::Value::Lit(Json::String(pattern))) => pattern.clone(),
                    _ => return Err(mismatch("a string pattern in the generated source")),
                };
                // The model states its patterns in RE2, which the regex crate
                // is, so an uncompilable pattern is nearly unreachable — but it
                // is an ordinary six-field mismatch in every backend
                // (compat/model/README.md § Assertions), never an exception out
                // of the evaluator, and the phrase is the same in all of them.
                let regex = match regex::Regex::new(&pattern) {
                    Ok(regex) => regex,
                    Err(err) => {
                        return Err(self.fail(
                            observed,
                            step,
                            &full_kind,
                            check.path,
                            &format!("pattern {pattern}"),
                            &failure::quote(&format!("unsupported pattern: {err}")),
                        ))
                    }
                };
                let hit = resolved
                    .and_then(Json::as_str)
                    .is_some_and(|value| regex.is_match(value));
                if !hit {
                    return Err(mismatch(&format!("a string matching {pattern:?}")));
                }
            }
        }
        Ok(())
    }

    fn fail(
        &self,
        observed: &Observed,
        step: &str,
        kind: &str,
        path: &str,
        expected: &str,
        actual: &str,
    ) -> Failure {
        self.fail_with(
            observed.op,
            &observed.params,
            step,
            kind,
            path,
            expected,
            actual,
            false,
        )
    }

    /// Reports a call that should have succeeded. The SDK's error text is
    /// quoted verbatim as the actual value, so the reader sees what the SDK
    /// said, and the 501 classification is decided here — the one place holding
    /// the status the emulator answered with.
    fn failed_call(&self, observed: &Observed, step: &str) -> Failure {
        let error = observed.error.as_ref();
        let actual = error
            .map(|error| failure::quote(&error.display))
            .unwrap_or_else(|| "\"\"".to_string());
        self.fail_with(
            observed.op,
            &observed.params,
            step,
            "call",
            "",
            "the call to succeed",
            &actual,
            error.is_some_and(SdkFailure::is_unimplemented),
        )
    }

    #[allow(clippy::too_many_arguments)]
    fn fail_with(
        &self,
        op: &str,
        params: &str,
        step: &str,
        kind: &str,
        path: &str,
        expected: &str,
        actual: &str,
        unimplemented: bool,
    ) -> Failure {
        Failure {
            group: self.group.name.to_string(),
            test: self.test.clone(),
            op: op.to_string(),
            params: params.to_string(),
            kind: kind.to_string(),
            path: path.to_string(),
            expected: expected.to_string(),
            actual: actual.to_string(),
            file: self.group.file.to_string(),
            step: step.to_string(),
            prefix: String::new(),
            unimplemented,
        }
    }
}
