

- Runui set title from the configuration, including using environemnt varialbes (Like git revision)

- Given the new usage pattern, gorun may be redundant and folded into execrun (maybe a specific command, maybe not even that)

# Service status

Service is hierarchical, and provides a modular way to describe. It is thread safe.
There are four status: OK, RUNNING_WITH_ERRORS, DEGRADED, DOWN

The global Status is the worst of all status reported.
Globally you have an API to CreateServiceStatus(critical bool) (by default it is ok)

SetStatus() will change the status, but a non critical service error is adjusted to no worse than RUNNING_WITH_ERRORS. See below on additional parameters

Status contains the following fields:

- Service name
- Status
- Time in state
- Additional data (json) from the last update (SetStatus has an `any`)
- Change history (last 10 changes)
- Uptime (percentage in error state)

Status: - Global Status (and because of which serrvice) - List of status per services

# Operator Mode

Just as we can execute runctl with build mode only, I also want to be able to execute in operator mode.

## The function of an operator

# execrun perspective

Execrun usually performs build and execution. In operator mode it performs only execution, but also
remote-update.

## How update remote affects execution?

remote-update affects execution very subtly: there is an additional template variable VERSION_DIR, which
allows the command to perform:

```yaml
exec:
    - {{ .CURRENT_VERSION }}/bin/runfile -c config.yml
```

## How versions are managed

1. execrun maintains three versions in its local folders, representing versions it needs to run.

- `stable.txt`: the current stable version
- `current.txt`: the last downloaded versitron.
- `rejects.txt`: a list of versions that were rejected, and should not be downloaded

These files are in its run folder when running in operator mode, and provide a persistent mechansims for running.

2. It polls the remote source for new versions. It also fillows redirects. For example, if it is running
   version `acd1` and polls for new versions in the url `http://remote/prefix/mytarget`:

- If the content is `abd1`, it is up to date
- If the content is `abd2`, it needs to update
- If the content is `@otherlocation` it will poll for a new version on `http://remote/prefix/otherlocation`, and continue recursively until it receives a real version to match

4. When a new version is available it is downloaded and opened (the versions are zipped) into folder by its name. `latest.txt`, is updated to this version.

5. Execrun attempts to execute it by assigning the `current.tx` value to the `CURRENT_VERSION` variable, which makes it available to run from the new folder. If it is ok (doesn't crash and doesn't have terminal service status) after `stability_time`, that new version will be updated in the `stable.txt`.

6. If a `current.txt` version has errors, it will append that version to `rejects.txt` (see below), update the `current.txt` and `CURRENT_VERSION` with the stable, and restart. If a stable version crashes a sufficient number of time it backs off in respawning it exponentially until 10m.

**version changes and decisions are communicated on the output file and stdout by execrun, as it does today on process start stop etc**

## Optional `FORCE_STABLE` invocation variable.

Execrun obeys a `FORCE_STABLE` variable, which makes it execute the stable version, and not download or promote new versions (only check)

## Rejects file

Includes reject version, time of reject, and comment about why (crashes, service status, etc)

## Configuration updates

- `stability_time`: how long until the version is considered stable, default: 5m
- `remote.base_url`: remote base url
  `remote.target`: the initial suffix of the remote url, actual initial test will be `remote.base_url`/`remote.target`
- `remote.secret`
- `remote.polling_interval`: how often to poll for new versions, default: 1m

# Reusable internal state and metrics tools

Build a reusable package, similar in spirit to `chiutil`, for components that
own their own runtime state and can expose it through several views. This should
not be centered on `backoffice`, though backoffice can later consume it.

The motivation is that Prometheus metrics are good as an export format, but are
not a good source of truth for in-process decisions. Incrementing a Prometheus
counter is easy; reading it back or using it as local state is awkward and
discouraged. Instead, application objects should keep local state with normal Go
APIs, and Prometheus should be one encoder over snapshots.

Design direction:

- Define stateful metric/check objects that implement a small interface.
- Register those objects in a central registry.
- Let each object own its concurrency and return immutable snapshots.
- Let the registry safely enumerate snapshots.
- Provide Prometheus-like scrape output from snapshots.
- Also support JSON/debug views and local policy decisions from the same data.

Candidate abstractions:

- `Registry`: central collection of checks/metrics.
- `Check`: a component, dependency, subsystem, or aggregate that can report a
  state snapshot.
- `CheckSnapshot`: name, severity, criticality, message, data, children, metrics.
- `Metric`: stateful value object that can be snapshotted.
- `MetricSnapshot`: name, kind, help, samples.
- `Gauge`: `Set`, `Add`, `Get`, `Snapshot`.
- `Counter`: `Inc`, `Add`, `Get`, `Snapshot`.
- `GaugeVec` / tuple gauge: labeled values with safe enumeration.
- `Distribution` / histogram: `Observe`, count, sum, buckets, min/max, later
  quantiles if needed.
- `Threshold`: evaluates a metric/distribution and contributes `pass`, `warn`,
  `fail`, or `down` to a check.

This should align with the Internal State principle:

- Components own their condition.
- Checks form a hierarchy.
- Aggregates calculate conclusions from their children.
- Metrics are evidence and decision inputs, not just exported numbers.
- Missing state should be treated differently from a metric that merely stopped
  arriving.

Possible API sketch:

```go
reg := state.NewRegistry("issuer")

latency := state.NewDistribution("upstream_latency_seconds").
    Threshold(state.P95GreaterThan(0.5, state.Warn)).
    Threshold(state.P99GreaterThan(2.0, state.Fail))

errors := state.NewCounter("upstream_errors_total")

upstream := state.NewCheck("upstream-token-service",
    state.Critical(),
    state.WithMetric(latency),
    state.WithMetric(errors),
)

reg.Register(upstream)

start := time.Now()
err := callUpstream()
latency.Observe(time.Since(start).Seconds())
if err != nil {
    errors.Inc()
}

if upstream.Snapshot().Severity >= state.Fail {
    // degrade, switch upstream, shed work, etc.
}

http.Handle("/metrics", state.PrometheusHandler(reg))
http.Handle("/state", state.JSONHandler(reg))
```

Implementation notes:

- Avoid depending on `prometheus/client_golang` in the first pass.
- Emit Prometheus text exposition directly from snapshots.
- Keep the core package HTTP-framework-neutral.
- Add chi/backoffice integration as adapters, not as the core.
- Start with simple exact locking/atomics before optimizing.
