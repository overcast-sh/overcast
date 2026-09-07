use std::collections::{HashMap, HashSet};
use std::fs;
use std::io::ErrorKind;
use std::path::{Path, PathBuf};

use serde::Deserialize;

use crate::harness::{TestCase, TestFn, TestGroup};

/// Machine-generated sibling of `registry.json`, written wholly by
/// `cmd/compatgen` from the scenario IR. Every loader concatenates the two.
const GENERATED_REGISTRY_FILE: &str = "registry.generated.json";

/// The only `version` this loader understands. Anything else is a load error
/// rather than a file we half-read.
const GENERATED_REGISTRY_VERSION: u32 = 1;

#[derive(Deserialize)]
struct RegistryRoot {
    groups: Vec<RegistryGroup>,
}

#[derive(Deserialize)]
struct RegistryGroup {
    service: String,
    name: String,
    /// Restricts the group to the suites it names; empty — the usual case —
    /// means every suite. See [`in_scope`].
    #[serde(default)]
    suites: Vec<String>,
    /// The group's tests may run concurrently with one another. Only a
    /// generated probe group carries it — see [`crate::harness::TestGroup`].
    #[serde(default)]
    parallel: bool,
    tests: Vec<RegistryTest>,
    /// Set only for groups read from [`GENERATED_REGISTRY_FILE`]; the
    /// hand-written registry declares none of those fields, so serde never
    /// populates this.
    #[serde(skip)]
    generated: Option<GeneratedMeta>,
}

/// The metadata a generated group carries beyond the shared group shape.
///
/// Its presence is what makes a group generated, which is the loader's one
/// branch on it ([`build_test_case`]); its two values are read by
/// [`missing_scenario_backend`], the message a reader gets when a group this
/// suite is scoped to resolves to nothing.
struct GeneratedMeta {
    /// `candidate` or `gated`, verbatim. The gate semantics are `cmd/compat`'s,
    /// not a suite's, so no value is rejected here.
    state: String,
    /// Path to the scenario IR the group was generated from, relative to the
    /// repository root. Optional in the schema.
    scenario: Option<String>,
}

#[derive(Clone, Deserialize)]
struct RegistryTest {
    name: String,
    op: Option<String>,
    skip: Option<String>,
    #[serde(default)]
    requires: Vec<String>,
    #[serde(default)]
    depends: Vec<String>,
}

/// The generated registry's own shape. Its three extra fields are required
/// here — not `Option`, no `#[serde(default)]` — so serde's missing-field error
/// is the load error a malformed generated file has to produce.
#[derive(Deserialize)]
struct GeneratedRoot {
    version: u32,
    groups: Vec<GeneratedGroup>,
}

#[derive(Deserialize)]
struct GeneratedGroup {
    service: String,
    name: String,
    tests: Vec<RegistryTest>,
    /// Always `true` in a well-formed file. Read rather than assumed: a group
    /// that does not claim to be generator output must not be loaded as one.
    generated: bool,
    state: String,
    suites: Vec<String>,
    #[serde(default)]
    parallel: bool,
    #[serde(default)]
    scenario: Option<String>,
}

impl GeneratedGroup {
    fn into_registry_group(self, path: &Path) -> Result<RegistryGroup, String> {
        if !self.generated {
            return Err(format!(
                "parse {}: group \"{}\" has \"generated\": false — every group in this file is generator output",
                path.display(),
                self.name
            ));
        }
        Ok(RegistryGroup {
            service: self.service,
            name: self.name,
            suites: self.suites,
            parallel: self.parallel,
            tests: self.tests,
            generated: Some(GeneratedMeta {
                state: self.state,
                scenario: self.scenario,
            }),
        })
    }
}

/// The test a scenario backend is asked to execute, and the group it belongs
/// to.
///
/// Built for any test with no static impl — a generated group's, or a
/// hand-written group ported to an authored scenario under the same registry
/// group/test names (G6, docs/plans/compat-coverage-modelgen.md § 3.11) — not
/// only a generated group's.
///
/// Group and test are the whole of it, because they are the whole of what a
/// resolution needs: [`crate::scenario::Backend`] looks the pair up in the map
/// the emitted source registered. A backend that needs more than the pair gets
/// it added then, with the reader that wants it — a field nothing reads is not
/// an extension point, it is dead weight behind an `#[allow(dead_code)]`.
pub struct ScenarioRequest<'a> {
    pub group: &'a str,
    pub test: &'a str,
}

/// Resolves a test to an implementation when no static impl claims it.
///
/// A scenario-backed group — generated, or a hand-written group ported to an
/// authored scenario under the same registry group/test names (G6,
/// docs/plans/compat-coverage-modelgen.md § 3.11) — is executed by an
/// interpreter walking its scenario IR, not by an implementation a service
/// file registered, so it needs a resolution step of its own. This is that
/// extension point, and it is consulted for **any** test with no static impl
/// — hand-written or generated — before the not-implemented sentinels below.
/// It is the last one tried: the registry `skip`, the capability gate and the
/// registered impls all still win over it.
///
/// [`crate::scenario::Backend`] implements it, over the typed calls
/// `cmd/compatgen` emits into `src/groups/scenarios_*_gen.rs`. Where no backend
/// is passed at all, or one resolves nothing, a generated group scoped to this
/// suite fails loudly — see [`missing_scenario_backend`] — and a hand-written
/// group with no impl keeps today's plain `not yet implemented` skip.
pub trait ScenarioBackend {
    fn resolve(&self, request: &ScenarioRequest<'_>) -> Option<TestFn>;
}

/// Everything about the owning group that a test's resolution depends on.
struct GroupContext<'a> {
    name: &'a str,
    generated: Option<&'a GeneratedMeta>,
}

pub fn build_groups(
    suite: &str,
    impls: &HashMap<String, TestFn>,
    setups: &HashMap<String, TestFn>,
    teardowns: &HashMap<String, TestFn>,
    capabilities: &HashSet<String>,
    backend: Option<&dyn ScenarioBackend>,
) -> Result<Vec<TestGroup>, String> {
    let registry = load()?;
    assemble(
        suite,
        registry,
        impls,
        setups,
        teardowns,
        capabilities,
        backend,
    )
}

fn assemble(
    suite: &str,
    registry: RegistryRoot,
    impls: &HashMap<String, TestFn>,
    setups: &HashMap<String, TestFn>,
    teardowns: &HashMap<String, TestFn>,
    capabilities: &HashSet<String>,
    backend: Option<&dyn ScenarioBackend>,
) -> Result<Vec<TestGroup>, String> {
    validate_impls(&registry, impls, suite)?;
    let ambiguous = ambiguous_test_names(&registry);

    Ok(registry
        .groups
        .into_iter()
        // A group scoped to specific suites (`suites` in the registry) is out
        // of scope for every other suite: no tests, no skips, no results. The
        // rule is general and covers both halves of the registry — see
        // [`in_scope`].
        .filter(|group| in_scope(group, suite))
        .map(|group| {
            let RegistryGroup {
                service,
                name,
                tests,
                generated,
                parallel,
                ..
            } = group;
            let context = GroupContext {
                name: &name,
                generated: generated.as_ref(),
            };
            let tests: Vec<TestCase> = topo_sort(tests)
                .into_iter()
                .map(|test| {
                    build_test_case(
                        suite,
                        &context,
                        test,
                        impls,
                        capabilities,
                        &ambiguous,
                        backend,
                    )
                })
                .collect();
            TestGroup {
                suite: suite.to_string(),
                setup: setups.get(&name).cloned(),
                teardown: teardowns.get(&name).cloned(),
                service,
                name,
                parallel,
                tests,
            }
        })
        .collect())
}

/// Test names that appear in more than one registry group.
///
/// A bare-name implementation cannot serve these: `CreateFunction` belongs to
/// both `lambda-crud` and `appsync-functions`, so falling back to the bare name
/// runs whichever group registered it last. That is how rust-sdk — which has no
/// AppSync implementation at all — reported results for `appsync-functions`,
/// answering with Lambda's CreateFunction. A suite must register the
/// group-qualified key for these; anything else is not implemented.
fn ambiguous_test_names(registry: &RegistryRoot) -> HashSet<String> {
    test_name_owners(registry)
        .into_iter()
        .filter(|(_, groups)| groups.len() > 1)
        .map(|(name, _)| name.to_string())
        .collect()
}

/// Maps each registry test name to the sorted groups that declare it.
fn test_name_owners(registry: &RegistryRoot) -> std::collections::BTreeMap<&str, Vec<&str>> {
    let mut owners: std::collections::BTreeMap<&str, Vec<&str>> = Default::default();
    for group in &registry.groups {
        for test in &group.tests {
            let groups = owners.entry(test.name.as_str()).or_default();
            if !groups.contains(&group.name.as_str()) {
                groups.push(group.name.as_str());
            }
        }
    }
    for groups in owners.values_mut() {
        groups.sort_unstable();
    }
    owners
}

fn build_test_case(
    suite: &str,
    group: &GroupContext<'_>,
    test: RegistryTest,
    impls: &HashMap<String, TestFn>,
    capabilities: &HashSet<String>,
    ambiguous: &HashSet<String>,
    backend: Option<&dyn ScenarioBackend>,
) -> TestCase {
    let noop: TestFn = std::sync::Arc::new(|_| Box::pin(async { Ok(()) }));

    if let Some(skip) = test.skip.clone() {
        return TestCase {
            name: test.name,
            op: test.op,
            skip: Some(skip),
            depends: test.depends,
            fn_: noop,
        };
    }

    if !test.requires.is_empty()
        && test
            .requires
            .iter()
            .any(|required| !capabilities.contains(required))
    {
        return TestCase {
            name: test.name,
            op: test.op,
            skip: Some(format!(
                "requires {} (not available in this environment)",
                test.requires.join(", ")
            )),
            depends: test.depends,
            fn_: noop,
        };
    }

    // A name shared by several groups may only be resolved by its qualified
    // key — a bare-name match would run another group's implementation.
    let qualified = format!("{}:{}", group.name, test.name);
    let bare = if ambiguous.contains(&test.name) {
        None
    } else {
        impls.get(&test.name).cloned()
    };
    if let Some(implementation) = impls.get(&qualified).cloned().or(bare) {
        return TestCase {
            name: test.name,
            op: test.op,
            skip: None,
            depends: test.depends,
            fn_: implementation,
        };
    }

    // No registered impl. Consulted for any test with no static impl — a
    // generated group's, or a hand-written group ported to an authored
    // scenario under the same registry group/test names (G6,
    // docs/plans/compat-coverage-modelgen.md § 3.11) — not only a generated
    // group's, so a G6-ported hand-written group resolves to its scenario
    // with no loader change. Only a generated group with no resolution falls
    // to the interim fail rule; a hand-written one keeps today's plain
    // not-yet-implemented skip.
    let resolved = backend.and_then(|backend| {
        backend.resolve(&ScenarioRequest {
            group: group.name,
            test: &test.name,
        })
    });

    if let Some(implementation) = resolved {
        return TestCase {
            name: test.name,
            op: test.op,
            skip: None,
            depends: test.depends,
            fn_: implementation,
        };
    }

    match group.generated {
        Some(meta) => TestCase {
            name: test.name,
            op: test.op,
            skip: None,
            depends: test.depends,
            fn_: missing_scenario_backend(group.name, meta, suite),
        },
        None => TestCase {
            name: test.name,
            op: test.op,
            skip: Some(format!("not yet implemented in {suite} test suite")),
            depends: test.depends,
            fn_: noop,
        },
    }
}

/// The interim result for a generated group this suite is scoped to but has no
/// backend for.
///
/// `suites` on a generated group is derived from backend availability by
/// `cmd/compatgen`, so a suite named in it that cannot execute the group is a
/// generator or loader bug, and it has to be loud. Reporting `skip` would file
/// it under the same sentinel a hand-written registry gap uses, and `na` would
/// claim the SDK has no API for it — both report as success, which is the one
/// thing this must never do. So the test fails the way any other broken
/// expectation does: the harness turns an `Err` from a test into a `fail`
/// result, so no new result kind is needed. Because `candidate` groups are
/// excluded from the gates by `cmd/compat` (#1367) this cannot red a build until
/// a group is `gated`, at which point it is a real regression and should.
fn missing_scenario_backend(group: &str, meta: &GeneratedMeta, suite: &str) -> TestFn {
    // The state and the scenario path are what a reader needs next: whether the
    // group counts against a gate, and which file to look in.
    let message = format!(
        "generated group \"{group}\" ({state}, from {scenario}) is scoped to {suite} \
         but {suite} has no scenario backend",
        state = meta.state,
        scenario = meta.scenario.as_deref().unwrap_or("an unnamed scenario"),
    );
    std::sync::Arc::new(move |_| {
        let message = message.clone();
        Box::pin(async move { Err(message) })
    })
}

fn load() -> Result<RegistryRoot, String> {
    let path = registry_path();
    let mut registry: RegistryRoot = read_json(&path)?;
    let generated = read_generated(&generated_sibling(&path))?;
    append_generated(&mut registry, generated)?;
    Ok(registry)
}

fn registry_path() -> PathBuf {
    std::env::var("OVERCAST_REGISTRY_PATH")
        .map(PathBuf::from)
        .unwrap_or_else(|_| PathBuf::from("../registry.json"))
}

/// The generated registry is the sibling of `registry.json` — same directory,
/// whatever path this loader was pointed at.
fn generated_sibling(registry: &Path) -> PathBuf {
    registry.with_file_name(GENERATED_REGISTRY_FILE)
}

fn read_json<T: serde::de::DeserializeOwned>(path: &Path) -> Result<T, String> {
    let body = fs::read_to_string(path).map_err(|err| format!("read {}: {err}", path.display()))?;
    serde_json::from_str(&body).map_err(|err| format!("parse {}: {err}", path.display()))
}

/// Reads the generated registry.
///
/// A missing file is an empty registry, never an error: suite images, CI
/// artifacts and branches cut before the file existed all have to keep working.
/// A file that is present and wrong is a load error, exactly as a malformed
/// `registry.json` is — a bad generated file must not be silently dropped.
fn read_generated(path: &Path) -> Result<Vec<RegistryGroup>, String> {
    let body = match fs::read_to_string(path) {
        Ok(body) => body,
        Err(err) if err.kind() == ErrorKind::NotFound => return Ok(Vec::new()),
        Err(err) => return Err(format!("read {}: {err}", path.display())),
    };
    parse_generated(&body, path)
}

fn parse_generated(body: &str, path: &Path) -> Result<Vec<RegistryGroup>, String> {
    let root: GeneratedRoot =
        serde_json::from_str(body).map_err(|err| format!("parse {}: {err}", path.display()))?;
    if root.version != GENERATED_REGISTRY_VERSION {
        return Err(format!(
            "parse {}: unsupported version {} (this loader reads {GENERATED_REGISTRY_VERSION})",
            path.display(),
            root.version
        ));
    }
    root.groups
        .into_iter()
        .map(|group| group.into_registry_group(path))
        .collect()
}

/// Concatenates the generated groups onto the hand-written ones — hand-written
/// first, generated after, in file order — dropping the ones this suite is out
/// of scope for.
///
/// The two files are joined on group and test names, so a generated group may
/// never reuse a hand-written name: `cmd/compat` lints for it and this is the
/// second line of defence, the same posture as the ambiguous-name defence in
/// [`validate_impls`].
///
/// This concatenates and validates only. `suites` scoping is applied once, over
/// the joined registry, in [`assemble`] — a generated group out of scope here is
/// no different from `cdk-lifecycle` out of scope in an SDK suite, and both
/// halves are filtered by the same rule.
fn append_generated(
    registry: &mut RegistryRoot,
    generated: Vec<RegistryGroup>,
) -> Result<(), String> {
    let hand_written: HashSet<String> = registry
        .groups
        .iter()
        .map(|group| group.name.clone())
        .collect();
    let collisions = {
        let mut names: Vec<String> = generated
            .iter()
            .filter(|group| hand_written.contains(&group.name))
            .map(|group| group.name.clone())
            .collect();
        names.sort_unstable();
        names.dedup();
        names
    };
    if !collisions.is_empty() {
        return Err(format!(
            "{GENERATED_REGISTRY_FILE} redeclares group(s) already in registry.json: {} — \
             the two files are joined on group and test names, so a generated group may not \
             reuse a hand-written name",
            collisions.join(", ")
        ));
    }

    registry.groups.extend(generated);
    Ok(())
}

/// Whether this suite runs `group`.
///
/// A group declaring `suites` runs only in the suites it names; elsewhere it is
/// out of scope rather than in debt, which is what keeps a suite with no
/// scenario backend from reporting anything at all about a group it was never
/// asked to run. The rule is the same for both halves of the registry: a
/// generated group's list is derived from backend availability by
/// `cmd/compatgen`, and on a hand-written group it is reserved for
/// `cdk-lifecycle` — which is why applying it here stopped rust-sdk reporting
/// that group's 35 skips and re-seeded its baseline shard (#1737).
fn in_scope(group: &RegistryGroup, suite: &str) -> bool {
    group.suites.is_empty() || group.suites.iter().any(|scoped| scoped == suite)
}

fn topo_sort(tests: Vec<RegistryTest>) -> Vec<RegistryTest> {
    let by_name: HashMap<_, _> = tests
        .iter()
        .map(|test| (test.name.clone(), test.clone()))
        .collect();
    let mut visited = HashSet::new();
    let mut visiting = HashSet::new();
    let mut sorted = Vec::with_capacity(tests.len());

    for test in &tests {
        visit(
            &test.name,
            &by_name,
            &mut visited,
            &mut visiting,
            &mut sorted,
        );
    }

    sorted
}

fn visit(
    name: &str,
    by_name: &HashMap<String, RegistryTest>,
    visited: &mut HashSet<String>,
    visiting: &mut HashSet<String>,
    sorted: &mut Vec<RegistryTest>,
) {
    if visited.contains(name) || visiting.contains(name) {
        return;
    }

    let Some(test) = by_name.get(name) else {
        return;
    };

    visiting.insert(name.to_string());
    for dependency in &test.depends {
        visit(dependency, by_name, visited, visiting, sorted);
    }
    visiting.remove(name);
    visited.insert(name.to_string());
    sorted.push(test.clone());
}

/// Flattens the per-service impl maps into the single map the loader resolves
/// against, refusing any key that two sources both register.
///
/// The merge used to be `impls.extend(group.impls())` — last writer wins, and
/// silently. Two service files both registering `lambda-crud:CreateFunction`
/// left one implementation unreachable with nothing said about it, and the run
/// reported a result for whichever one survived. [`validate_impls`] cannot
/// catch this: by the time it sees the flattened map the discarded
/// implementation is already gone, and the surviving key resolves perfectly
/// well.
///
/// Sources are `(label, impls)` in registration order; the label is the service
/// file, so a collision can name both sides.
pub fn merge_impls<'a>(
    sources: impl IntoIterator<Item = (&'a str, HashMap<String, TestFn>)>,
    suite: &str,
) -> Result<HashMap<String, TestFn>, String> {
    let mut merged: HashMap<String, TestFn> = HashMap::new();
    let mut owner: HashMap<String, &str> = HashMap::new(); // key → first registrant

    let mut problems = Vec::new();
    for (label, impls) in sources {
        for (key, implementation) in impls {
            if let Some(first) = owner.get(key.as_str()) {
                problems.push(duplicate_problem(&key, first, label));
                continue;
            }
            owner.insert(key.clone(), label);
            merged.insert(key, implementation);
        }
    }

    if problems.is_empty() {
        return Ok(merged);
    }
    // HashMap iteration order is unspecified, so sort for a stable message.
    // Every problem starts with the key, which is what a reader scans for.
    problems.sort_unstable();
    Err(format!(
        "[{suite}] {} duplicate impl registration(s):\n  - {}",
        problems.len(),
        problems.join("\n  - ")
    ))
}

/// One collision. The two sources are the same when a single service file
/// registers the key twice.
fn duplicate_problem(key: &str, first: &str, second: &str) -> String {
    let where_ = if first == second {
        format!("is registered twice by \"{first}\"")
    } else {
        format!("is registered by both \"{first}\" and \"{second}\"")
    };
    format!(
        "impl \"{key}\" {where_} — one of the two would be silently discarded; \
         remove or re-key one"
    )
}

/// Rejects impl keys that cannot be bound to exactly one registry test.
///
/// This used to be a stderr warning nobody read, while the test the key was
/// meant to implement quietly fell back to another group's implementation and
/// reported a pass. Two registrations are refused:
///
/// - a key matching no registry entry — a typo, a stale name, or the wrong
///   separator (every suite uses `group:test`; `group/test` is not accepted);
/// - a bare key for a test name that several groups declare, which cannot say
///   which group it implements.
fn validate_impls(
    registry: &RegistryRoot,
    impls: &HashMap<String, TestFn>,
    suite: &str,
) -> Result<(), String> {
    let owners = test_name_owners(registry);
    let names: HashSet<_> = registry
        .groups
        .iter()
        .flat_map(|group| {
            group
                .tests
                .iter()
                .flat_map(|test| [test.name.clone(), format!("{}:{}", group.name, test.name)])
        })
        .collect();

    let mut keys: Vec<&String> = impls.keys().collect();
    keys.sort();

    let mut problems = Vec::new();
    for name in keys {
        if !names.contains(name) {
            let mut msg = format!("impl \"{name}\" matches no registry entry");
            if name.contains('/') {
                // The Java suite used "group/test" until the separator was
                // unified; a key copied from it resolves to nothing here.
                msg.push_str(&format!(
                    " (group-qualified keys use \":\", not \"/\" — did you mean \"{}\"?)",
                    name.replacen('/', ":", 1)
                ));
            }
            problems.push(msg);
        } else if let Some(groups) = owners.get(name.as_str()) {
            if groups.len() > 1 {
                // Naming every candidate rather than guessing one: only the
                // author knows which group this implementation is for, and
                // binding it to the wrong one is what this check prevents.
                let candidates: Vec<String> = groups
                    .iter()
                    .map(|group| format!("\"{group}:{name}\""))
                    .collect();
                problems.push(format!(
                    "impl \"{name}\" is ambiguous: groups {groups:?} all declare a test named                      \"{name}\" — qualify it with the group it implements, one of: {}",
                    candidates.join(", ")
                ));
            }
        }
    }

    if problems.is_empty() {
        return Ok(());
    }
    Err(format!(
        "[{suite}] {} unusable impl registration(s):
  - {}",
        problems.len(),
        problems.join("
  - ")
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    use crate::harness::TestContext;

    fn noop() -> TestFn {
        std::sync::Arc::new(|_| Box::pin(async { Ok(()) }))
    }

    fn test_entry(name: &str) -> RegistryTest {
        RegistryTest {
            name: name.to_string(),
            op: None,
            skip: None,
            requires: Vec::new(),
            depends: Vec::new(),
        }
    }

    fn hand_written_group(service: &str, name: &str, tests: Vec<RegistryTest>) -> RegistryGroup {
        RegistryGroup {
            service: service.to_string(),
            name: name.to_string(),
            suites: Vec::new(),
            parallel: false,
            tests,
            generated: None,
        }
    }

    /// Two unrelated groups declaring a test of the same name, plus a name owned
    /// by exactly one group — the shape that made a mis-binding possible.
    fn two_groups_one_name() -> RegistryRoot {
        RegistryRoot {
            groups: vec![
                hand_written_group(
                    "iam",
                    "iam-users",
                    vec![test_entry("ListUsers"), test_entry("CreateUser")],
                ),
                hand_written_group(
                    "cognito",
                    "cognito-userpools",
                    vec![test_entry("ListUsers")],
                ),
            ],
        }
    }

    fn impls_with(keys: &[&str]) -> HashMap<String, TestFn> {
        keys.iter().map(|k| (k.to_string(), noop())).collect()
    }

    fn err_of(keys: &[&str]) -> String {
        validate_impls(&two_groups_one_name(), &impls_with(keys), "rust-sdk")
            .expect_err("expected the registration to be refused")
    }

    #[test]
    fn rejects_key_written_with_the_old_slash_separator() {
        let err = err_of(&["iam-users/CreateUser"]);
        assert!(err.contains("iam-users/CreateUser"), "{err}");
        assert!(err.contains("matches no registry entry"), "{err}");
        // The message must point at the colon form, since that is the fix.
        assert!(err.contains("iam-users:CreateUser"), "{err}");
    }

    #[test]
    fn rejects_keys_naming_an_unknown_group_or_test() {
        assert!(err_of(&["iam-usres:CreateUser"]).contains("iam-usres:CreateUser"));
        assert!(err_of(&["CreateUsr"]).contains("CreateUsr"));
    }

    #[test]
    fn rejects_bare_key_for_a_name_several_groups_declare() {
        let err = err_of(&["ListUsers"]);
        assert!(err.contains("ambiguous"), "{err}");
        assert!(err.contains("iam-users"), "{err}");
        assert!(err.contains("cognito-userpools"), "{err}");
    }

    #[test]
    fn accepts_resolvable_keys() {
        let impls = impls_with(&[
            "CreateUser", // bare, single owner
            "iam-users:ListUsers",
            "cognito-userpools:ListUsers",
        ]);
        validate_impls(&two_groups_one_name(), &impls, "rust-sdk").expect("should be accepted");
    }

    /// Merges the sources and returns the refusal message. Not `expect_err`:
    /// the Ok type is a map of `TestFn`, which is not `Debug`.
    fn merge_err<'a>(
        sources: impl IntoIterator<Item = (&'a str, HashMap<String, TestFn>)>,
    ) -> String {
        match merge_impls(sources, "rust-sdk") {
            Ok(_) => panic!("expected the duplicate registration to be refused"),
            Err(err) => err,
        }
    }

    /// Two service files registering the same key must abort the run.
    ///
    /// This is the gap `validate_impls` cannot close. The merge that builds the
    /// suite's impl map is last-writer-wins, so one of the two implementations
    /// is discarded before validation ever sees the map — and the surviving key
    /// resolves perfectly well, so nothing is reported. The discarded test then
    /// runs the other file's implementation under its own name.
    #[test]
    fn rejects_key_registered_by_two_sources() {
        let err = merge_err([
            ("lambda", impls_with(&["lambda-crud:CreateFunction"])),
            ("appsync", impls_with(&["lambda-crud:CreateFunction"])),
        ]);

        assert!(err.contains("duplicate impl registration"), "{err}");
        assert!(err.contains("lambda-crud:CreateFunction"), "{err}");
        // Both registering files must be named: the key alone does not say
        // where to look, and one of the two files is in the wrong.
        assert!(err.contains("\"lambda\""), "{err}");
        assert!(err.contains("\"appsync\""), "{err}");
    }

    /// "both X and Y" would be nonsense when X and Y are the same file.
    #[test]
    fn reports_single_source_duplicate_as_such() {
        let err = merge_err([
            ("iam", impls_with(&["iam-users:CreateUser"])),
            ("iam", impls_with(&["iam-users:CreateUser"])),
        ]);

        assert!(err.contains("registered twice by \"iam\""), "{err}");
    }

    /// Fixing one duplicate must not merely reveal the next.
    #[test]
    fn reports_every_duplicate() {
        let keys = ["iam-users:CreateUser", "iam-users:ListUsers"];
        let err = merge_err([("iam", impls_with(&keys)), ("cognito", impls_with(&keys))]);

        assert!(err.contains("2 duplicate impl registration(s)"), "{err}");
        // Sorted by key, so the message is stable despite HashMap ordering.
        assert!(
            err.find("iam-users:CreateUser") < err.find("iam-users:ListUsers"),
            "problems not sorted by key: {err}"
        );
    }

    /// Negative control: distinct keys merge cleanly and all survive.
    #[test]
    fn accepts_disjoint_sources() {
        let merged = merge_impls(
            [
                ("iam", impls_with(&["iam-users:ListUsers", "CreateUser"])),
                ("cognito", impls_with(&["cognito-userpools:ListUsers"])),
            ],
            "rust-sdk",
        )
        .expect("disjoint sources should merge");

        assert_eq!(merged.len(), 3);
        for key in [
            "iam-users:ListUsers",
            "CreateUser",
            "cognito-userpools:ListUsers",
        ] {
            assert!(merged.contains_key(key), "merged map is missing {key}");
        }
    }

    /// Builds the fixture's groups *without* [`validate_impls`], which is the
    /// point: the resolution tests below check that a bad key cannot bind even
    /// when validation is bypassed.
    fn build(keys: &[&str]) -> Vec<TestGroup> {
        let registry = two_groups_one_name();
        let ambiguous = ambiguous_test_names(&registry);
        let impls = impls_with(keys);
        registry
            .groups
            .into_iter()
            .map(|group| TestGroup {
                suite: "rust-sdk".to_string(),
                service: group.service.clone(),
                name: group.name.clone(),
                parallel: group.parallel,
                tests: group
                    .tests
                    .iter()
                    .map(|test| {
                        build_test_case(
                            "rust-sdk",
                            &GroupContext {
                                name: &group.name,
                                generated: group.generated.as_ref(),
                            },
                            test.clone(),
                            &impls,
                            &HashSet::new(),
                            &ambiguous,
                            None,
                        )
                    })
                    .collect(),
                setup: None,
                teardown: None,
            })
            .collect()
    }

    fn skip_of(groups: &[TestGroup], group: &str, test: &str) -> Option<String> {
        groups
            .iter()
            .find(|g| g.name == group)
            .and_then(|g| g.tests.iter().find(|t| t.name == test))
            .unwrap_or_else(|| panic!("no test {group}/{test} in built groups"))
            .skip
            .clone()
    }

    /// Defence in depth: even bypassing validation, no cross-group binding.
    #[test]
    fn refuses_cross_group_bare_fallback() {
        let groups = build(&["ListUsers"]);
        assert_eq!(
            skip_of(&groups, "cognito-userpools", "ListUsers").as_deref(),
            Some("not yet implemented in rust-sdk test suite")
        );
        assert!(skip_of(&groups, "iam-users", "ListUsers").is_some());
    }

    #[test]
    fn binds_qualified_key_to_its_group_only() {
        let groups = build(&["iam-users:ListUsers"]);
        assert!(skip_of(&groups, "iam-users", "ListUsers").is_none());
        assert!(skip_of(&groups, "cognito-userpools", "ListUsers").is_some());
    }

    #[test]
    fn allows_unambiguous_bare_fallback() {
        let groups = build(&["CreateUser"]);
        assert!(skip_of(&groups, "iam-users", "CreateUser").is_none());
    }

    #[test]
    fn tracks_owners_of_each_name() {
        let registry = two_groups_one_name();
        assert!(ambiguous_test_names(&registry).contains("ListUsers"));
        assert!(!ambiguous_test_names(&registry).contains("CreateUser"));
        assert_eq!(
            test_name_owners(&registry).get("ListUsers"),
            Some(&vec!["cognito-userpools", "iam-users"])
        );
    }

    // ── registry.generated.json ──────────────────────────────────────────────

    /// The body `compat/suites/registry.generated.json` ships with. Asserted as
    /// a *no-op*, not as what is on disk: the generator is expected to fill that
    /// file in, and a test pinning it empty would fail the day it does.
    const EMPTY_GENERATED: &str = r#"{"version":1,"groups":[]}"#;

    fn generated_path() -> PathBuf {
        PathBuf::from("../registry.generated.json")
    }

    /// A generated file declaring one group per `(name, suites)` pair, each with
    /// a single `SendMessage` test.
    fn generated_json(groups: &[(&str, &[&str])]) -> String {
        let groups: Vec<String> = groups
            .iter()
            .map(|(name, suites)| {
                let suites: Vec<String> =
                    suites.iter().map(|suite| format!("\"{suite}\"")).collect();
                format!(
                    r#"{{"service":"sqs","name":"{name}","generated":true,"state":"candidate","scenario":"compat/scenarios/{name}.yaml","suites":[{}],"tests":[{{"name":"SendMessage"}}]}}"#,
                    suites.join(",")
                )
            })
            .collect();
        format!(r#"{{"version":1,"groups":[{}]}}"#, groups.join(","))
    }

    fn group_names(registry: &RegistryRoot) -> Vec<&str> {
        registry
            .groups
            .iter()
            .map(|group| group.name.as_str())
            .collect()
    }

    /// Loads `body` as the generated registry and concatenates it onto the
    /// hand-written fixture, as [`load`] does with the two real files.
    fn concatenated(body: &str) -> Result<RegistryRoot, String> {
        let mut registry = two_groups_one_name();
        let generated = parse_generated(body, &generated_path())?;
        append_generated(&mut registry, generated)?;
        Ok(registry)
    }

    /// `parallel` is carried from the generated file onto the group the harness
    /// runs. Only a probe group sets it, and it is the one field of a generated
    /// group that changes how a run is *scheduled* rather than what it reports —
    /// so a loader that dropped it would slow a run down silently.
    #[test]
    fn the_parallel_flag_reaches_the_group_the_harness_runs() {
        let body = format!(
            r#"{{"version":1,"groups":[{},{}]}}"#,
            r#"{"service":"sqs","name":"sqs-gen-probe","generated":true,"state":"candidate","parallel":true,"suites":["rust-sdk"],"tests":[{"name":"ListMessageMoveTasks"}]}"#,
            r#"{"service":"sqs","name":"sqs-gen-queue","generated":true,"state":"candidate","suites":["rust-sdk"],"tests":[{"name":"CreateQueue"}]}"#,
        );
        let registry = concatenated(&body).expect("generated registry loads");
        let groups = assemble(
            "rust-sdk",
            registry,
            &HashMap::new(),
            &HashMap::new(),
            &HashMap::new(),
            &HashSet::new(),
            None,
        )
        .expect("groups assemble");

        let parallel: Vec<(&str, bool)> = groups
            .iter()
            .map(|group| (group.name.as_str(), group.parallel))
            .collect();
        assert!(
            parallel.contains(&("sqs-gen-probe", true)),
            "the probe group is not marked parallel: {parallel:?}"
        );
        assert!(
            parallel.contains(&("sqs-gen-queue", false)),
            "a lifecycle group was marked parallel: {parallel:?}"
        );
    }

    #[test]
    fn resolves_the_generated_registry_beside_registry_json() {
        // Whatever path the loader was pointed at — the default relative one,
        // or the absolute OVERCAST_REGISTRY_PATH the suite image sets.
        assert_eq!(
            generated_sibling(Path::new("../registry.json")),
            PathBuf::from("../registry.generated.json")
        );
        assert_eq!(
            generated_sibling(Path::new("/registry.json")),
            PathBuf::from("/registry.generated.json")
        );
    }

    /// Suite images, CI artifacts and branches cut before the file existed all
    /// have to keep working, so a missing file is an empty registry.
    #[test]
    fn a_missing_generated_registry_is_a_no_op() {
        let absent = generated_path().with_file_name("registry.generated.absent.json");
        let generated = read_generated(&absent).expect("a missing file is not an error");
        assert!(generated.is_empty());

        let mut registry = two_groups_one_name();
        let before = group_names(&registry).join(",");
        append_generated(&mut registry, generated).expect("nothing to concatenate");
        assert_eq!(group_names(&registry).join(","), before);
    }

    #[test]
    fn an_empty_generated_registry_is_a_no_op() {
        let registry = concatenated(EMPTY_GENERATED).expect("empty file must load");
        assert_eq!(group_names(&registry), ["iam-users", "cognito-userpools"]);
    }

    #[test]
    fn generated_groups_follow_the_hand_written_ones_in_file_order() {
        let body = generated_json(&[
            ("sqs-scenario-b", &["rust-sdk"]),
            ("sqs-scenario-a", &["rust-sdk", "python-sdk"]),
        ]);
        let registry = concatenated(&body).expect("generated file must load");
        assert_eq!(
            group_names(&registry),
            [
                "iam-users",
                "cognito-userpools",
                "sqs-scenario-b",
                "sqs-scenario-a"
            ]
        );
    }

    /// Out of scope is not the same as in debt: the group contributes no tests,
    /// no skips and no results to a suite its `suites` list does not name.
    #[test]
    fn a_generated_group_scoped_elsewhere_is_not_loaded() {
        let body = generated_json(&[("sqs-scenario", &["python-sdk", "node-js-sdk"])]);
        let registry = concatenated(&body).expect("generated file must load");
        assert_eq!(
            assembled_names(registry, "rust-sdk"),
            ["iam-users", "cognito-userpools"]
        );
    }

    /// The same rule on the other half of the registry (#1737). `cdk-lifecycle`
    /// is the only hand-written group that declares `suites`, and rust-sdk is
    /// not in that list, so it is not loaded here either — no tests, no skips,
    /// no results, which is what took its 35 rows out of
    /// `compat/baseline/rust-sdk.json`.
    #[test]
    fn a_hand_written_group_scoped_elsewhere_is_not_loaded() {
        let mut registry = two_groups_one_name();
        registry.groups.push(RegistryGroup {
            service: "cdk".to_string(),
            name: "cdk-lifecycle".to_string(),
            suites: vec!["cdk".to_string()],
            parallel: false,
            tests: vec![test_entry("Deploy")],
            generated: None,
        });
        assert_eq!(
            assembled_names(registry, "rust-sdk"),
            ["iam-users", "cognito-userpools"]
        );
    }

    /// The group names [`assemble`] builds for `suite`, which is where `suites`
    /// scoping is applied.
    fn assembled_names(registry: RegistryRoot, suite: &str) -> Vec<String> {
        assemble(
            suite,
            registry,
            &HashMap::new(),
            &HashMap::new(),
            &HashMap::new(),
            &HashSet::new(),
            None,
        )
        .expect("groups must build")
        .into_iter()
        .map(|group| group.name)
        .collect()
    }

    /// The interim rule: scoped to this suite, no impl, no backend ⇒ a `fail`
    /// carrying the message every loader emits, never a skip and never an `na`.
    #[tokio::test]
    async fn a_scoped_generated_group_without_a_backend_fails_loudly() {
        let body = generated_json(&[("sqs-scenario", &["rust-sdk"])]);
        let registry = concatenated(&body).expect("generated file must load");
        let groups = assemble(
            "rust-sdk",
            registry,
            &HashMap::new(),
            &HashMap::new(),
            &HashMap::new(),
            &HashSet::new(),
            None,
        )
        .expect("groups must build");

        let group = groups
            .iter()
            .find(|group| group.name == "sqs-scenario")
            .expect("the generated group must be loaded");
        let test = group
            .tests
            .first()
            .expect("the generated group must keep its test");

        assert!(
            test.skip.is_none(),
            "must not report as a skip: {:?}",
            test.skip
        );
        let err = (test.fn_)(TestContext::new(
            "http://localhost:4566".to_string(),
            "us-east-1".to_string(),
            "test".to_string(),
        ))
        .await
        .expect_err("a group with no backend must fail");
        assert_eq!(
            err,
            "generated group \"sqs-scenario\" (candidate, from compat/scenarios/sqs-scenario.yaml) \
             is scoped to rust-sdk but rust-sdk has no scenario backend"
        );
        // The harness classifies an Err by its text; this one must land as
        // `fail`, not be swallowed as an emulator gap.
        assert!(!crate::harness::looks_unimplemented_without_tag(&err), "{err}");
    }

    /// The two files are joined on group and test names. `cmd/compat` lints for
    /// this; the loader is the second line of defence.
    #[test]
    fn a_generated_group_may_not_reuse_a_hand_written_name() {
        let body = generated_json(&[("iam-users", &["rust-sdk"])]);
        // Not `expect_err`: the Ok type is a RegistryRoot, which is not Debug.
        let err = concatenated(&body)
            .err()
            .expect("the collision must be refused");
        assert!(err.contains("iam-users"), "{err}");
    }

    /// A file that is present and wrong is a load error, exactly as a malformed
    /// registry.json is — never a silent empty registry.
    #[test]
    fn a_malformed_generated_registry_is_a_load_error() {
        for (label, body) in [
            ("unparsable", "{".to_string()),
            (
                "wrong version",
                r#"{"version":2,"groups":[]}"#.to_string(),
            ),
            ("no version", r#"{"groups":[]}"#.to_string()),
            (
                "group without state",
                r#"{"version":1,"groups":[{"service":"sqs","name":"sqs-scenario","generated":true,"suites":["rust-sdk"],"tests":[{"name":"SendMessage"}]}]}"#.to_string(),
            ),
            (
                "group without suites",
                r#"{"version":1,"groups":[{"service":"sqs","name":"sqs-scenario","generated":true,"state":"candidate","tests":[{"name":"SendMessage"}]}]}"#.to_string(),
            ),
            (
                "group without generated",
                r#"{"version":1,"groups":[{"service":"sqs","name":"sqs-scenario","state":"candidate","suites":["rust-sdk"],"tests":[{"name":"SendMessage"}]}]}"#.to_string(),
            ),
        ] {
            let err = parse_generated(&body, &generated_path())
                .err()
                .unwrap_or_else(|| panic!("{label} must not load"));
            assert!(
                err.contains("registry.generated.json"),
                "{label}: message must name the file: {err}"
            );
        }
    }

    /// A stand-in backend that answers every request, so a case can show the
    /// extension point is reached and with which group and test.
    struct RecordingBackend;

    impl ScenarioBackend for RecordingBackend {
        fn resolve(&self, request: &ScenarioRequest<'_>) -> Option<TestFn> {
            let described = format!("{}/{}", request.group, request.test);
            let implementation: TestFn = std::sync::Arc::new(move |_| {
                let described = described.clone();
                Box::pin(async move { Err(described) })
            });
            Some(implementation)
        }
    }

    #[tokio::test]
    async fn a_backend_resolves_a_generated_group_before_the_fail() {
        let body = generated_json(&[("sqs-scenario", &["rust-sdk"])]);
        let registry = concatenated(&body).expect("generated file must load");
        let groups = assemble(
            "rust-sdk",
            registry,
            &HashMap::new(),
            &HashMap::new(),
            &HashMap::new(),
            &HashSet::new(),
            Some(&RecordingBackend),
        )
        .expect("groups must build");

        let test = groups
            .iter()
            .find(|group| group.name == "sqs-scenario")
            .and_then(|group| group.tests.first())
            .expect("the generated group must be loaded");
        let resolved = (test.fn_)(TestContext::new(
            "http://localhost:4566".to_string(),
            "us-east-1".to_string(),
            "test".to_string(),
        ))
        .await
        .expect_err("the stand-in reports what it was given");
        assert_eq!(resolved, "sqs-scenario/SendMessage");
    }

    /// G6 (docs/plans/compat-coverage-modelgen.md § 3.11) ports hand-written
    /// groups to authored scenarios under the same registry group/test
    /// names, so the backend must be consulted for a hand-written group's
    /// test with no impl too — not only a generated group's.
    #[tokio::test]
    async fn a_hand_written_groups_test_resolves_through_a_registered_backend() {
        let registry = two_groups_one_name();
        let groups = assemble(
            "rust-sdk",
            registry,
            &HashMap::new(),
            &HashMap::new(),
            &HashMap::new(),
            &HashSet::new(),
            Some(&RecordingBackend),
        )
        .expect("groups must build");

        let test = groups
            .iter()
            .find(|group| group.name == "iam-users")
            .and_then(|group| group.tests.iter().find(|t| t.name == "CreateUser"))
            .expect("the hand-written group's test must be loaded");

        assert!(
            test.skip.is_none(),
            "must not fall back to the not-yet-implemented skip: {:?}",
            test.skip
        );

        let resolved = (test.fn_)(TestContext::new(
            "http://localhost:4566".to_string(),
            "us-east-1".to_string(),
            "test".to_string(),
        ))
        .await
        .expect_err("the stand-in reports what it was given");
        assert_eq!(resolved, "iam-users/CreateUser");
    }

    /// `scenario` is optional in the schema, and a group omitting it still
    /// loads.
    #[test]
    fn a_generated_group_may_omit_its_scenario_path() {
        let body = r#"{"version":1,"groups":[{"service":"sqs","name":"sqs-scenario","generated":true,"state":"gated","suites":["rust-sdk"],"tests":[{"name":"SendMessage"}]}]}"#;
        let groups = parse_generated(body, &generated_path()).expect("must load");
        let meta = groups[0]
            .generated
            .as_ref()
            .expect("the group must carry its metadata");
        assert_eq!(meta.state, "gated");
        assert!(meta.scenario.is_none());
    }
}
