use std::collections::HashMap;
use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use aws_smithy_runtime_api::client::orchestrator::HttpResponse;
use aws_smithy_runtime_api::client::result::SdkError;
use aws_smithy_types::error::display::DisplayErrorContext;
use aws_smithy_types::error::metadata::ProvideErrorMetadata;
use serde::{Deserialize, Serialize};
use tokio::io::{AsyncBufReadExt, BufReader};
use tokio::sync::{OwnedSemaphorePermit, Semaphore};

pub type TestFuture = Pin<Box<dyn Future<Output = Result<(), String>> + Send>>;
pub type TestFn = Arc<dyn Fn(TestContext) -> TestFuture + Send + Sync>;

/// Render an AWS SDK error as the string a test returns, classified.
///
/// Two things happen here, and the first is why every `map_err` in this suite
/// names this function. `SdkError`'s own `Display` is the single word "service
/// error" — the error code, message and request id all live further down the
/// `source()` chain. Reporting only the head made every rust-sdk failure in the
/// compat baseline unactionable: nine tests said "service error" and nothing
/// else, so the one thing a compat suite exists to tell you — how the emulator
/// diverged — was exactly what got dropped. `DisplayErrorContext` walks the
/// chain, which is what the AWS SDK's own examples use for logging.
///
/// The second is the classification. A test's error is a `String` by the time
/// [`classify`] sees it, and this is the last place the response itself is in
/// hand — so the answer is stated here, as a tag, rather than guessed at later
/// by looking for "501" in the text. That guess was the bug: a request id, an
/// ARN, a resource name or a port is enough to put those digits in a 400's
/// message, and the sibling go-sdk suite reported a test asserting an
/// `InvalidRequestException` as `unimplemented` on one CI run, flipping a gated
/// baseline row and failing an unrelated pull request (#1924).
///
/// Use [`sdk_error_message`] instead where the text is quoted **inside** a
/// larger message: a tag is only read at the front of the string a test returns.
pub fn sdk_error<E>(err: SdkError<E, HttpResponse>) -> String
where
    E: std::error::Error + ProvideErrorMetadata + Send + Sync + 'static,
{
    let message = format!("{}", DisplayErrorContext(&err));
    match classify_sdk_error(&err) {
        Some(true) => format!("{UNIMPLEMENTED_TAG}{message}"),
        Some(false) => format!("{FAIL_TAG}{message}"),
        None => message,
    }
}

/// [`sdk_error`]'s rendering without the classification tag.
///
/// Two callers want it. One quotes the SDK's text **inside** a message of its
/// own, where a tag would land in the middle of the string and be read by
/// nobody. The other holds an error that never reached the wire and so has no
/// response to classify from — a `BuildError` from an operation's own input
/// validation, say — which [`sdk_error`]'s signature excludes for exactly that
/// reason.
pub fn sdk_error_message(err: impl std::error::Error + Send + Sync + 'static) -> String {
    format!("{}", DisplayErrorContext(&err))
}

/// Decide, from the HTTP response an SDK error carries, whether the emulator
/// refused the operation as unimplemented. `None` when the error carries no
/// response — the SDK failed before or after the exchange — which is the one
/// case [`classify`]'s substring heuristic is for.
///
/// Two things say "unimplemented", and both are facts of the response rather
/// than of its wording: HTTP 501, with the `x-emulator-unsupported` header
/// Overcast sets alongside every one of them; and an error **code** of
/// `NotImplemented` or `UnknownOperationException`, by equality. AWS — and
/// Overcast — answer a target naming no modeled operation with the latter at
/// HTTP 400, so the status alone would miss it.
fn classify_sdk_error<E: ProvideErrorMetadata>(err: &SdkError<E, HttpResponse>) -> Option<bool> {
    let response = err.raw_response()?;
    if response.status().as_u16() == 501 {
        return Some(true);
    }
    if response
        .headers()
        .get("x-emulator-unsupported")
        .is_some_and(|value| value.eq_ignore_ascii_case("true"))
    {
        return Some(true);
    }
    let code = match err {
        SdkError::ServiceError(context) => context.err().code(),
        _ => None,
    };
    Some(matches!(
        code,
        Some("NotImplemented") | Some("UnknownOperationException")
    ))
}

#[derive(Clone)]
pub struct TestContext {
    pub endpoint: Arc<String>,
    pub region: Arc<String>,
    pub run_id: Arc<String>,
    state: Arc<Mutex<HashMap<String, String>>>,
}

impl TestContext {
    pub fn new(endpoint: String, region: String, run_id: String) -> Self {
        Self {
            endpoint: Arc::new(endpoint),
            region: Arc::new(region),
            run_id: Arc::new(run_id),
            state: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    pub fn set(&self, key: &str, value: String) {
        if let Ok(mut state) = self.state.lock() {
            state.insert(key.to_string(), value);
        }
    }

    pub fn get(&self, key: &str) -> Option<String> {
        self.state
            .lock()
            .ok()
            .and_then(|state| state.get(key).cloned())
    }

    pub fn log(&self, msg: &str) {
        eprintln!("[rust-sdk] {msg}");
    }
}

#[derive(Clone)]
pub struct TestCase {
    pub name: String,
    pub op: Option<String>,
    pub skip: Option<String>,
    pub depends: Vec<String>,
    pub fn_: TestFn,
}

#[derive(Clone)]
pub struct TestGroup {
    pub suite: String,
    pub service: String,
    pub name: String,
    /// The group's tests may run concurrently with one another, taking their
    /// slots from the same semaphore concurrent groups take theirs from — so
    /// the bound on a whole run is that slot count, not the square of it.
    ///
    /// Only a generated probe group sets it (`parallel` in
    /// `registry.generated.json`): a probe has no setup, no teardown and no
    /// exports, so no test can create, consume or observe anything another one
    /// touches. Results are still emitted in the group's own test order,
    /// whatever order the calls finished in — the dashboard, the baseline and
    /// the flake detector all read that stream.
    pub parallel: bool,
    pub tests: Vec<TestCase>,
    pub setup: Option<TestFn>,
    pub teardown: Option<TestFn>,
}

#[derive(Serialize)]
struct RunStartEvent<'a> {
    event: &'a str,
    suite: &'a str,
    started_at: String,
    endpoint: &'a str,
    version: &'a str,
    total_tests: usize,
}

#[derive(Serialize)]
struct TestResultEvent<'a> {
    event: &'a str,
    suite: &'a str,
    service: &'a str,
    group: &'a str,
    test: &'a str,
    status: &'a str,
    duration_ms: u128,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
}

#[derive(Serialize)]
struct RunEndEvent<'a> {
    event: &'a str,
    suite: &'a str,
    passed: usize,
    failed: usize,
    skipped: usize,
    unimplemented: usize,
    duration_ms: u128,
}

pub async fn run_suite(suite: &str, endpoint: &str, region: &str, groups: Vec<TestGroup>) {
    let started = Instant::now();
    emit(&RunStartEvent {
        event: "run_start",
        suite,
        started_at: chrono_like_now(),
        endpoint,
        version: "1",
        total_tests: groups.iter().map(|group| group.tests.len()).sum(),
    });

    // One semaphore for the whole run. A group holds a slot while it runs, and
    // a parallel group hands its own back while its tests take one each (see
    // run_group), so what this bounds is the work in flight rather than the
    // groups in flight.
    let slots = Arc::new(Semaphore::new(parallel_slots()));

    let mut handles = Vec::new();
    for group in groups {
        let permit = slots
            .clone()
            .acquire_owned()
            .await
            .expect("semaphore closed");
        let endpoint = endpoint.to_string();
        let region = region.to_string();
        let suite = suite.to_string();
        let slots = slots.clone();
        handles.push(tokio::spawn(async move {
            run_group(&suite, &endpoint, &region, group, slots, permit).await
        }));
    }

    let mut passed = 0;
    let mut failed = 0;
    let mut skipped = 0;
    let mut unimplemented = 0;

    for handle in handles {
        if let Ok((group_passed, group_failed, group_skipped, group_unimplemented)) = handle.await {
            passed += group_passed;
            failed += group_failed;
            skipped += group_skipped;
            unimplemented += group_unimplemented;
        }
    }

    emit(&RunEndEvent {
        event: "run_end",
        suite,
        passed,
        failed,
        skipped,
        unimplemented,
        duration_ms: started.elapsed().as_millis(),
    });
}

/// Runs one group, holding the run-wide slot `run_suite` acquired for it.
///
/// The permit is passed in rather than held by the caller because a parallel
/// group has to give it back for the duration of its fan-out — see below.
async fn run_group(
    suite: &str,
    endpoint: &str,
    region: &str,
    group: TestGroup,
    slots: Arc<Semaphore>,
    permit: OwnedSemaphorePermit,
) -> (usize, usize, usize, usize) {
    let mut permit = Some(permit);
    let run_id = std::env::var("OVERCAST_COMPAT_RUN_ID").unwrap_or_else(|_| "local".to_string());
    let context = TestContext::new(endpoint.to_string(), region.to_string(), run_id);
    let mut passed = 0;
    let mut failed = 0;
    let mut skipped = 0;
    let mut unimplemented = 0;

    if let Some(setup) = group.setup.clone() {
        if let Err(err) = setup(context.clone()).await {
            let reason = format!("setup failed: {}", strip_tag(&err));
            for test in &group.tests {
                emit(&TestResultEvent {
                    event: "test_result",
                    suite,
                    service: &group.service,
                    group: &group.name,
                    test: &test.name,
                    status: "skip",
                    duration_ms: 0,
                    error: Some(reason.clone()),
                });
                skipped += 1;
            }
            run_teardown(&group, context).await;
            return (passed, failed, skipped, unimplemented);
        }
    }

    if group.parallel && group.tests.iter().all(|test| test.depends.is_empty()) {
        // Hand this group's own slot back before fanning out. The tests take
        // theirs from the same semaphore, so keeping it would put the real
        // bound at slots squared — and, once every slot were held by a group
        // waiting on its own tests, no test could ever acquire one.
        drop(permit.take());
        let (p, f, s, u) = run_tests_concurrently(suite, &group, &context, &slots).await;
        passed += p;
        failed += f;
        skipped += s;
        unimplemented += u;
        // Teardown is this group's work again, so it runs holding a slot.
        permit = slots.acquire_owned().await.ok();
        run_teardown(&group, context).await;
        drop(permit);
        return (passed, failed, skipped, unimplemented);
    }

    let mut blocked = std::collections::HashSet::new();
    for test in &group.tests {
        if let Some(skip) = &test.skip {
            emit(&TestResultEvent {
                event: "test_result",
                suite,
                service: &group.service,
                group: &group.name,
                test: &test.name,
                status: "skip",
                duration_ms: 0,
                error: Some(skip.clone()),
            });
            skipped += 1;
            blocked.insert(test.name.clone());
            continue;
        }

        let failed_deps: Vec<_> = test
            .depends
            .iter()
            .filter(|dependency| blocked.contains(*dependency))
            .cloned()
            .collect();
        if !failed_deps.is_empty() {
            emit(&TestResultEvent {
                event: "test_result",
                suite,
                service: &group.service,
                group: &group.name,
                test: &test.name,
                status: "skip",
                duration_ms: 0,
                error: Some(format!("dependency failed: {}", failed_deps.join(", "))),
            });
            skipped += 1;
            blocked.insert(test.name.clone());
            continue;
        }

        let started = Instant::now();
        match (test.fn_)(context.clone()).await {
            Ok(()) => {
                emit(&TestResultEvent {
                    event: "test_result",
                    suite,
                    service: &group.service,
                    group: &group.name,
                    test: &test.name,
                    status: "pass",
                    duration_ms: started.elapsed().as_millis(),
                    error: None,
                });
                passed += 1;
            }
            Err(err) => {
                let (status, message) = classify(&err);
                emit(&TestResultEvent {
                    event: "test_result",
                    suite,
                    service: &group.service,
                    group: &group.name,
                    test: &test.name,
                    status,
                    duration_ms: started.elapsed().as_millis(),
                    error: Some(message),
                });
                if status == "unimplemented" {
                    unimplemented += 1;
                } else {
                    failed += 1;
                }
                blocked.insert(test.name.clone());
            }
        }
    }

    run_teardown(&group, context).await;
    drop(permit);
    (passed, failed, skipped, unimplemented)
}

/// Runs a parallel group's tests concurrently and emits their results in the
/// group's own test order.
///
/// The order is what makes the flag safe: it is the wall clock that changes,
/// never the result stream. A group whose tests declare a dependency is run in
/// order regardless — the IR never produces that combination, and honouring the
/// flag over an edge would be running a dependency after its dependent.
///
/// `slots` is the run's own semaphore, not one of this function's making: the
/// tests of a parallel group and the groups of a run draw on one budget, which
/// is what makes `TestGroup::parallel`'s "the same slot count" true.
async fn run_tests_concurrently(
    suite: &str,
    group: &TestGroup,
    context: &TestContext,
    slots: &Arc<Semaphore>,
) -> (usize, usize, usize, usize) {
    let mut handles = Vec::with_capacity(group.tests.len());
    for test in &group.tests {
        if test.skip.is_some() {
            handles.push(None);
            continue;
        }
        let permit = slots
            .clone()
            .acquire_owned()
            .await
            .expect("semaphore closed");
        let run = test.fn_.clone();
        let context = context.clone();
        handles.push(Some(tokio::spawn(async move {
            let _permit = permit;
            let started = Instant::now();
            let outcome = run(context).await;
            (outcome, started.elapsed().as_millis())
        })));
    }

    let mut passed = 0;
    let mut failed = 0;
    let mut skipped = 0;
    let mut unimplemented = 0;
    for (test, handle) in group.tests.iter().zip(handles) {
        let Some(handle) = handle else {
            emit(&TestResultEvent {
                event: "test_result",
                suite,
                service: &group.service,
                group: &group.name,
                test: &test.name,
                status: "skip",
                duration_ms: 0,
                error: test.skip.clone(),
            });
            skipped += 1;
            continue;
        };
        let (outcome, duration_ms) = match handle.await {
            Ok(joined) => joined,
            Err(err) => (Err(format!("test task did not finish: {err}")), 0),
        };
        let (status, error) = match outcome {
            Ok(()) => ("pass", None),
            Err(err) => {
                let (status, message) = classify(&err);
                (status, Some(message))
            }
        };
        emit(&TestResultEvent {
            event: "test_result",
            suite,
            service: &group.service,
            group: &group.name,
            test: &test.name,
            status,
            duration_ms,
            error,
        });
        match status {
            "pass" => passed += 1,
            "unimplemented" => unimplemented += 1,
            _ => failed += 1,
        }
    }
    (passed, failed, skipped, unimplemented)
}

async fn run_teardown(group: &TestGroup, context: TestContext) {
    if let Some(teardown) = group.teardown.clone() {
        if let Err(err) = teardown(context).await {
            eprintln!(
                "[rust-sdk] teardown failed for {}: {}",
                group.name,
                strip_tag(&err)
            );
        }
    }
}

/// How many things this suite may do at once.
///
/// It bounds work, not groups: [`run_suite`] takes one slot per group from a
/// single semaphore, and a parallel group hands its own back while its tests
/// take one each, so the total in flight never exceeds this whichever shape the
/// run has. The interactive loop bounds its own concurrent runs the same way.
pub fn parallel_slots() -> usize {
    std::env::var("OVERCAST_COMPAT_PARALLEL_SLOTS")
        .ok()
        .and_then(|value| value.parse::<usize>().ok())
        .filter(|value| *value > 0)
        .unwrap_or(8)
}

/// The substring heuristic, and it is for a message that carries **no
/// classification tag** — an error a test wrote itself, or an SDK error that
/// never reached the wire, where there is no response to read.
///
/// It is never right for one that did reach the wire: the response states the
/// status, and "501" appears in request ids, ARNs, resource names and port
/// numbers. [`sdk_error`] tags those instead.
pub fn looks_unimplemented_without_tag(err: &str) -> bool {
    let err = err.to_ascii_lowercase();
    err.contains("501")
        || err.contains("notimplemented")
        || err.contains("unknownoperationexception")
        || err.contains("unknown action")
        || err.contains("not implemented")
}

/// A failure that states its own classification, rather than being guessed at.
///
/// `looks_unimplemented_without_tag` reads the whole message, and a message is
/// the wrong thing to read: a run id, a port number, a queue URL or an ARN can
/// put "501" in one that says nothing about the status, and the result would be
/// filed as `unimplemented` — a pass, in effect — instead of the failure it is.
/// A generated group's message makes that certain, because it embeds the exact
/// params JSON sent (compat/model/README.md § Failure messages), but #1924
/// showed a hand-written one is no safer.
///
/// So [`sdk_error`] and `crate::scenario` state the classification instead, as
/// a tag in front of the message. The tag is a control character no AWS error
/// text or params JSON contains, [`classify`] strips it before the message is
/// emitted, and it is the only way a message escapes the heuristic.
pub const UNIMPLEMENTED_TAG: &str = "\u{1}unimplemented\u{1}";

/// The same, for a failure that is a plain failure whatever its text contains.
pub const FAIL_TAG: &str = "\u{1}fail\u{1}";

/// The status a failed test reports, and the message to emit with it.
///
/// A tagged message says which it is; an untagged one — a message a test wrote
/// itself, with no SDK error behind it — falls back to the substring
/// heuristic.
pub fn classify(err: &str) -> (&'static str, String) {
    if let Some(rest) = err.strip_prefix(UNIMPLEMENTED_TAG) {
        return ("unimplemented", rest.to_string());
    }
    if let Some(rest) = err.strip_prefix(FAIL_TAG) {
        return ("fail", rest.to_string());
    }
    if looks_unimplemented_without_tag(err) {
        ("unimplemented", err.to_string())
    } else {
        ("fail", err.to_string())
    }
}

/// The message without its classification tag, for the places that report a
/// failure as something other than a test result — a setup failure folded into
/// every test's skip reason, and a teardown skip logged to stderr.
pub fn strip_tag(err: &str) -> &str {
    err.strip_prefix(UNIMPLEMENTED_TAG)
        .or_else(|| err.strip_prefix(FAIL_TAG))
        .unwrap_or(err)
}

fn emit<T: Serialize>(value: &T) {
    println!("{}", serde_json::to_string(value).expect("serialize event"));
}

fn chrono_like_now() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};

    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    format!("{}.{:09}Z", now.as_secs(), now.subsec_nanos())
}

// ── Interactive mode ──────────────────────────────────────────────────────────

#[derive(Serialize)]
struct BuildingEvent<'a> {
    event: &'a str,
    suite: &'a str,
    message: &'a str,
}

#[derive(Serialize)]
struct ReadyEvent<'a> {
    event: &'a str,
    suite: &'a str,
    total_tests: usize,
}

#[derive(Serialize)]
struct TestStartEvent<'a> {
    event: &'a str,
    suite: &'a str,
    service: &'a str,
    group: &'a str,
    test: &'a str,
}

#[derive(Serialize)]
struct CancelledEvent<'a> {
    event: &'a str,
    suite: &'a str,
    batch_id: &'a str,
    group: &'a str,
    test: &'a str,
    reason: &'a str,
}

#[derive(Serialize)]
struct BatchCompleteEvent<'a> {
    event: &'a str,
    suite: &'a str,
    batch_id: &'a str,
    passed: usize,
    failed: usize,
    skipped: usize,
    unimplemented: usize,
    cancelled: usize,
    duration_ms: u128,
}

#[derive(Deserialize)]
struct StdinCommand {
    command: String,
    #[serde(default)]
    batch_id: Option<String>,
    #[serde(default)]
    tests: Option<Vec<TestRef>>,
    #[serde(default)]
    group: Option<String>,
    #[serde(default)]
    test: Option<String>,
}

#[derive(Deserialize)]
struct TestRef {
    group: String,
    tests: Option<Vec<String>>,
}

type CancellationMap = Arc<Mutex<HashMap<String, Arc<AtomicBool>>>>;

pub async fn run_interactive(
    suite: &str,
    endpoint: &str,
    region: &str,
    all_groups: Vec<TestGroup>,
) {
    emit(&BuildingEvent {
        event: "building",
        suite,
        message: "Loading registry and building test groups...",
    });

    let total_tests: usize = all_groups.iter().map(|g| g.tests.len()).sum();
    emit(&ReadyEvent {
        event: "ready",
        suite,
        total_tests,
    });

    let semaphore = Arc::new(Semaphore::new(parallel_slots()));

    let cancellation_flags: CancellationMap = Arc::new(Mutex::new(HashMap::new()));

    // Build lookup map: group name → TestGroup
    let group_map: HashMap<String, TestGroup> = all_groups
        .into_iter()
        .map(|g| (g.name.clone(), g))
        .collect();
    let group_map = Arc::new(group_map);

    let stdin = tokio::io::stdin();
    let mut reader = BufReader::new(stdin).lines();

    while let Ok(Some(line)) = reader.next_line().await {
        let trimmed = line.trim().to_string();
        if trimmed.is_empty() {
            continue;
        }

        let cmd: StdinCommand = match serde_json::from_str(&trimmed) {
            Ok(c) => c,
            Err(e) => {
                eprintln!("[rust-sdk] invalid JSON on stdin: {trimmed} ({e})");
                continue;
            }
        };

        match cmd.command.as_str() {
            "run" => {
                handle_run(
                    cmd,
                    suite,
                    endpoint,
                    region,
                    group_map.clone(),
                    semaphore.clone(),
                    cancellation_flags.clone(),
                );
            }
            "cancel" => {
                handle_cancel(&cmd, &cancellation_flags);
            }
            "shutdown" => {
                // Cancel all in-flight work and exit.
                if let Ok(flags) = cancellation_flags.lock() {
                    for flag in flags.values() {
                        flag.store(true, Ordering::SeqCst);
                    }
                }
                return;
            }
            "ping" => {
                // Respond with pong + currently running test (if any).
                let rt = cancellation_flags
                    .lock()
                    .map(|flags| {
                        flags
                            .iter()
                            .find(|(_, flag)| !flag.load(Ordering::SeqCst))
                            .map(|(k, _)| k.clone())
                            .unwrap_or_default()
                    })
                    .unwrap_or_default();
                let ev = serde_json::json!({
                    "event": "pong",
                    "suite": suite,
                    "running_test": rt,
                });
                println!("{}", ev);
            }
            other => {
                eprintln!("[rust-sdk] unknown command: {other}");
            }
        }
    }
}

fn handle_run(
    cmd: StdinCommand,
    suite: &str,
    endpoint: &str,
    region: &str,
    group_map: Arc<HashMap<String, TestGroup>>,
    semaphore: Arc<Semaphore>,
    cancellation_flags: CancellationMap,
) {
    let batch_id = cmd.batch_id.unwrap_or_default();
    let test_refs = cmd.tests.unwrap_or_default();

    // Resolve requested groups/tests.
    // An empty test_refs list means "run all groups" (the run-all command).
    let mut groups_to_run = Vec::new();
    if test_refs.is_empty() {
        let mut all: Vec<_> = group_map.values().cloned().collect();
        all.sort_by(|a, b| a.name.cmp(&b.name));
        groups_to_run = all;
    } else {
        for test_ref in test_refs {
            let group = match group_map.get(&test_ref.group) {
                Some(g) => g.clone(),
                None => {
                    eprintln!(
                        "[rust-sdk] unknown group in run command: {}",
                        test_ref.group
                    );
                    continue;
                }
            };

            if let Some(tests) = test_ref.tests {
                if !tests.is_empty() {
                    let requested: std::collections::HashSet<String> = tests.into_iter().collect();
                    let mut filtered = group.clone();
                    filtered.tests.retain(|t| requested.contains(&t.name));
                    groups_to_run.push(filtered);
                    continue;
                }
            }
            groups_to_run.push(group);
        }
    }

    let suite = suite.to_string();
    let endpoint = endpoint.to_string();
    let region = region.to_string();

    // Fire off the batch asynchronously so stdin reading continues.
    tokio::spawn(async move {
        let batch_start = Instant::now();
        let mut handles = Vec::new();

        for group in groups_to_run {
            let permit = semaphore
                .clone()
                .acquire_owned()
                .await
                .expect("semaphore closed");
            let suite = suite.clone();
            let endpoint = endpoint.clone();
            let region = region.clone();
            let batch_id = batch_id.clone();
            let flags = cancellation_flags.clone();
            handles.push(tokio::spawn(async move {
                let _permit = permit;
                run_group_interactive(&suite, &endpoint, &region, group, &batch_id, flags).await
            }));
        }

        let mut passed = 0;
        let mut failed = 0;
        let mut skipped = 0;
        let mut unimplemented = 0;
        let mut cancelled = 0;

        for handle in handles {
            if let Ok((p, f, s, u, c)) = handle.await {
                passed += p;
                failed += f;
                skipped += s;
                unimplemented += u;
                cancelled += c;
            }
        }

        emit(&BatchCompleteEvent {
            event: "batch_complete",
            suite: &suite,
            batch_id: &batch_id,
            passed,
            failed,
            skipped,
            unimplemented,
            cancelled,
            duration_ms: batch_start.elapsed().as_millis(),
        });
    });
}

fn handle_cancel(cmd: &StdinCommand, cancellation_flags: &CancellationMap) {
    if let (Some(group), Some(test)) = (&cmd.group, &cmd.test) {
        let key = format!("{group}:{test}");
        if let Ok(flags) = cancellation_flags.lock() {
            if let Some(flag) = flags.get(&key) {
                flag.store(true, Ordering::SeqCst);
            }
        }
    } else if let Ok(flags) = cancellation_flags.lock() {
        for flag in flags.values() {
            flag.store(true, Ordering::SeqCst);
        }
    }
}

async fn run_group_interactive(
    suite: &str,
    endpoint: &str,
    region: &str,
    group: TestGroup,
    batch_id: &str,
    cancellation_flags: CancellationMap,
) -> (usize, usize, usize, usize, usize) {
    let run_id = std::env::var("OVERCAST_COMPAT_RUN_ID").unwrap_or_else(|_| "local".to_string());
    let context = TestContext::new(endpoint.to_string(), region.to_string(), run_id);
    let mut passed = 0;
    let mut failed = 0;
    let mut skipped = 0;
    let mut unimplemented = 0;
    let mut cancelled = 0;

    // Register cancellation flags for each test.
    {
        let mut flags = cancellation_flags.lock().unwrap();
        for test in &group.tests {
            let key = format!("{}:{}", group.name, test.name);
            flags.insert(key, Arc::new(AtomicBool::new(false)));
        }
    }

    // Setup phase
    let mut setup_ok = true;
    if let Some(setup) = group.setup.clone() {
        if let Err(err) = setup(context.clone()).await {
            let reason = format!("setup failed: {}", strip_tag(&err));
            for test in &group.tests {
                emit(&TestResultEvent {
                    event: "test_result",
                    suite,
                    service: &group.service,
                    group: &group.name,
                    test: &test.name,
                    status: "skip",
                    duration_ms: 0,
                    error: Some(reason.clone()),
                });
                skipped += 1;
            }
            setup_ok = false;
        }
    }

    // The interactive loop runs a parallel group's tests in order. An
    // interpreter that ignores the flag is still correct (compat/model/README.md
    // § The scenario file) — only the wall clock changes — and this path carries
    // per-test cancellation, which a concurrent one would have to answer for.
    if setup_ok {
        let mut blocked = std::collections::HashSet::new();
        for test in &group.tests {
            let key = format!("{}:{}", group.name, test.name);
            let is_cancelled = cancellation_flags
                .lock()
                .ok()
                .and_then(|flags| flags.get(&key).map(|f| f.load(Ordering::SeqCst)))
                .unwrap_or(false);

            if is_cancelled {
                emit(&CancelledEvent {
                    event: "cancelled",
                    suite,
                    batch_id,
                    group: &group.name,
                    test: &test.name,
                    reason: "user",
                });
                cancelled += 1;
                blocked.insert(test.name.clone());
                continue;
            }

            if let Some(skip) = &test.skip {
                emit(&TestResultEvent {
                    event: "test_result",
                    suite,
                    service: &group.service,
                    group: &group.name,
                    test: &test.name,
                    status: "skip",
                    duration_ms: 0,
                    error: Some(skip.clone()),
                });
                skipped += 1;
                blocked.insert(test.name.clone());
                continue;
            }

            let failed_deps: Vec<_> = test
                .depends
                .iter()
                .filter(|dep| blocked.contains(*dep))
                .cloned()
                .collect();
            if !failed_deps.is_empty() {
                emit(&TestResultEvent {
                    event: "test_result",
                    suite,
                    service: &group.service,
                    group: &group.name,
                    test: &test.name,
                    status: "skip",
                    duration_ms: 0,
                    error: Some(format!("dependency failed: {}", failed_deps.join(", "))),
                });
                skipped += 1;
                blocked.insert(test.name.clone());
                continue;
            }

            emit(&TestStartEvent {
                event: "test_start",
                suite,
                service: &group.service,
                group: &group.name,
                test: &test.name,
            });

            let started = Instant::now();
            match (test.fn_)(context.clone()).await {
                Ok(()) => {
                    let is_cancelled_after = cancellation_flags
                        .lock()
                        .ok()
                        .and_then(|flags| flags.get(&key).map(|f| f.load(Ordering::SeqCst)))
                        .unwrap_or(false);

                    if is_cancelled_after {
                        emit(&CancelledEvent {
                            event: "cancelled",
                            suite,
                            batch_id,
                            group: &group.name,
                            test: &test.name,
                            reason: "user",
                        });
                        cancelled += 1;
                        blocked.insert(test.name.clone());
                    } else {
                        emit(&TestResultEvent {
                            event: "test_result",
                            suite,
                            service: &group.service,
                            group: &group.name,
                            test: &test.name,
                            status: "pass",
                            duration_ms: started.elapsed().as_millis(),
                            error: None,
                        });
                        passed += 1;
                    }
                }
                Err(err) => {
                    let is_cancelled_after = cancellation_flags
                        .lock()
                        .ok()
                        .and_then(|flags| flags.get(&key).map(|f| f.load(Ordering::SeqCst)))
                        .unwrap_or(false);

                    if is_cancelled_after {
                        emit(&CancelledEvent {
                            event: "cancelled",
                            suite,
                            batch_id,
                            group: &group.name,
                            test: &test.name,
                            reason: "user",
                        });
                        cancelled += 1;
                    } else {
                        let (status, message) = classify(&err);
                        emit(&TestResultEvent {
                            event: "test_result",
                            suite,
                            service: &group.service,
                            group: &group.name,
                            test: &test.name,
                            status,
                            duration_ms: started.elapsed().as_millis(),
                            error: Some(message),
                        });
                        if status == "unimplemented" {
                            unimplemented += 1;
                        } else {
                            failed += 1;
                        }
                    }
                    blocked.insert(test.name.clone());
                }
            }
        }
    }

    // Always run teardown and clean up cancellation flags.
    run_teardown(&group, context).await;
    {
        let mut flags = cancellation_flags.lock().unwrap();
        for test in &group.tests {
            flags.remove(&format!("{}:{}", group.name, test.name));
        }
    }

    (passed, failed, skipped, unimplemented, cancelled)
}

#[cfg(test)]
mod classification_tests {
    use super::*;
    use aws_smithy_runtime_api::http::StatusCode;
    use aws_smithy_types::body::SdkBody;
    use aws_smithy_types::error::metadata::ErrorMetadata;

    /// A modeled operation error, as the SDK hands one back: an error code and
    /// a message, reachable through `ProvideErrorMetadata`.
    #[derive(Debug)]
    struct ModeledError(ErrorMetadata);

    impl std::fmt::Display for ModeledError {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            write!(
                f,
                "{}: {} (request id 5f2c9501-0f3a-4c7d-9a11-6b1d0c2e4a77)",
                self.0.code().unwrap_or_default(),
                self.0.message().unwrap_or_default()
            )
        }
    }

    impl std::error::Error for ModeledError {}

    impl ProvideErrorMetadata for ModeledError {
        fn meta(&self) -> &ErrorMetadata {
            &self.0
        }
    }

    /// The error shape a failed `send()` produces: the modeled error, and the
    /// raw response the exchange produced.
    fn service_error(
        code: &str,
        message: &str,
        status: u16,
        unsupported_header: bool,
    ) -> SdkError<ModeledError, HttpResponse> {
        let mut raw = HttpResponse::new(StatusCode::try_from(status).unwrap(), SdkBody::empty());
        if unsupported_header {
            raw.headers_mut().insert("x-emulator-unsupported", "true");
        }
        let meta = ErrorMetadata::builder().code(code).message(message).build();
        SdkError::service_error(ModeledError(meta), raw)
    }

    /// #1924: the classification reads the response, not the prose. The rule
    /// this replaced matched a bare "501" anywhere in the message, so a request
    /// id was enough to report a 400 as `unimplemented` — which is how the
    /// sibling go-sdk suite flipped a gated baseline row on CI run 34064243252
    /// and failed an unrelated pull request.
    #[test]
    fn a_400_is_a_failure_however_its_prose_reads() {
        let rendered = sdk_error(service_error(
            "InvalidRequestException",
            "No Lambda rotation function ARN is associated with this secret.",
            400,
            false,
        ));
        assert!(
            rendered.contains("501"),
            "the fixture must carry the digits that caused the bug: {rendered}"
        );
        assert_eq!(classify(&rendered).0, "fail");
    }

    #[test]
    fn a_400_whose_resource_name_contains_501_is_a_failure() {
        let rendered = sdk_error(service_error(
            "ResourceNotFoundException",
            "Secrets Manager can't find the specified secret: oc-501abcde-rotate",
            400,
            false,
        ));
        assert_eq!(classify(&rendered).0, "fail");
    }

    #[test]
    fn a_real_501_is_unimplemented() {
        let rendered = sdk_error(service_error(
            "NotImplemented",
            "This operation is not implemented by the emulator",
            501,
            true,
        ));
        assert_eq!(classify(&rendered).0, "unimplemented");
    }

    #[test]
    fn a_501_named_only_by_its_header_is_unimplemented() {
        let rendered = sdk_error(service_error("", "", 200, true));
        assert_eq!(classify(&rendered).0, "unimplemented");
    }

    #[test]
    fn an_unknown_operation_is_unimplemented_at_400() {
        let rendered = sdk_error(service_error(
            "UnknownOperationException",
            "Unknown operation: Frobnicate",
            400,
            false,
        ));
        assert_eq!(classify(&rendered).0, "unimplemented");
    }

    #[test]
    fn the_emitted_message_never_carries_the_tag() {
        let rendered = sdk_error(service_error(
            "NotImplemented",
            "not implemented",
            501,
            true,
        ));
        let (status, message) = classify(&rendered);
        assert_eq!(status, "unimplemented");
        assert!(
            !message.contains(UNIMPLEMENTED_TAG) && !message.contains(FAIL_TAG),
            "a tag must never reach the NDJSON error field: {message:?}"
        );
    }

    /// A message no SDK error produced — a test's own prose — still falls back
    /// to the heuristic, which is all it has.
    #[test]
    fn an_untagged_message_falls_back_to_the_heuristic() {
        assert_eq!(
            classify("CreateThing: 501 Not Implemented").0,
            "unimplemented"
        );
        assert_eq!(classify("CreateThing: arn is empty").0, "fail");
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::AtomicUsize;
    use std::time::Duration;

    /// A test case that records how many of its kind are running at once.
    fn gauge_case(name: &str, live: Arc<AtomicUsize>, peak: Arc<AtomicUsize>) -> TestCase {
        let fn_: TestFn = Arc::new(move |_context| {
            let live = live.clone();
            let peak = peak.clone();
            Box::pin(async move {
                let now = live.fetch_add(1, Ordering::SeqCst) + 1;
                peak.fetch_max(now, Ordering::SeqCst);
                tokio::time::sleep(Duration::from_millis(30)).await;
                live.fetch_sub(1, Ordering::SeqCst);
                Ok(())
            })
        });
        TestCase {
            name: name.to_string(),
            op: None,
            skip: None,
            depends: Vec::new(),
            fn_,
        }
    }

    /// The tests of a parallel group draw on the run's own semaphore, so two
    /// groups fanning out at once are still bounded by the slot count rather
    /// than by slots × slots.
    ///
    /// The bound is the claim `TestGroup::parallel` makes, and getting it wrong
    /// is invisible: nothing fails, the emulator just gets `slots²` calls at
    /// once.
    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn a_parallel_groups_tests_share_the_runs_slots() {
        const SLOTS: usize = 3;
        let live = Arc::new(AtomicUsize::new(0));
        let peak = Arc::new(AtomicUsize::new(0));
        let slots = Arc::new(Semaphore::new(SLOTS));
        let context = TestContext::new(
            "http://127.0.0.1:4566".to_string(),
            "us-east-1".to_string(),
            "run".to_string(),
        );
        let group = TestGroup {
            suite: "rust-sdk".to_string(),
            service: "widgets".to_string(),
            name: "widgets-gen-probe".to_string(),
            parallel: true,
            tests: (0..8)
                .map(|i| gauge_case(&format!("t{i}"), live.clone(), peak.clone()))
                .collect(),
            setup: None,
            teardown: None,
        };

        let (left, right) = tokio::join!(
            run_tests_concurrently("rust-sdk", &group, &context, &slots),
            run_tests_concurrently("rust-sdk", &group, &context, &slots),
        );
        assert_eq!(left.0 + right.0, 16, "every test must have run and passed");

        let peak = peak.load(Ordering::SeqCst);
        assert!(
            peak <= SLOTS,
            "{peak} tests ran at once with {SLOTS} slots: the fan-out is not sharing the run's semaphore"
        );
        assert!(
            peak > 1,
            "nothing ran concurrently, so this case would pass against a serial harness too"
        );
    }
}
