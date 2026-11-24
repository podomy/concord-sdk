<h1 align="center">Concord SDK</h1>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/github/license/podomy/concord-sdk" alt="License"></a>
</p>

Go client for Concord, the AP coordination runtime for robots that operate where links die.

This module is extracted from [podomy/concord](https://github.com/podomy/concord). Concord itself stays under AGPL-3.0; this SDK is licensed separately under Apache-2.0 to allow embedding in fleet applications.

## Install

```sh
go get github.com/podomy/concord-sdk@latest
```

Requires Go >= 1.26.3.

## Example

```go
client, err := sdk.Dial()
if err != nil {
    log.Fatalf("connect to concord daemon: %v", err)
}
defer client.Close()

spec, err := sdk.NewWorkload().
    Image("docker.io/library/nginx:alpine").
    Port(8080, 80).
    Env("ENV", "production").
    Restart(sdk.RestartAlways).
    CPUShares(1024).
    MemoryMB(512).
    HealthCheck("/healthz", sdk.HealthActionRestart).
    Build()
if err != nil {
    log.Fatalf("invalid spec: %v", err)
}

id, err := client.Submit(context.Background(), spec)
if err != nil {
    log.Fatalf("submit workload: %v", err)
}
fmt.Printf("Workload submitted: %s\n", id)
```

## API

| Method | Description |
| client.Submit(ctx, w) | Submit a workload, returns its UUID |
| client.Stop(ctx, id) | Stop and remove a workload |
| client.Get(ctx, id) | Fetch a workload spec by UUID |
| client.List(ctx) | List active workloads |
| client.Stats(ctx, id) | Live utilization for one workload |
| client.Logs(ctx, id, tail) | Captured container output, oldest-first (tail caps, 0 means all) |
| client.Metrics(ctx) | Sampler state in Prometheus exposition format |
| client.Trail(ctx) | This node's recorded positions since boot, oldest-first |
| client.SetPosition(ctx, lat, lon) | Apply one live position fix |
| client.UpdateImage(ctx, id, image) | Move a live workload to a new image |
| client.Nodes(ctx) | List cluster nodes |

Builder methods: Image, Command, Env, Envs, Port, Resources, MemoryMB, CPUShares, Restart, HealthCheck, HealthProbe, DependsOn, QoS, File, StopTimeout, StopTimeoutSeconds, Build, MustBuild.

File artifacts (`fileart`): push bytes with `fileart.NewClient(addr).Push(ctx, data)`, reference the digest with `File(path, digest, mode)`.

## Contributing

Concord SDK takes no pull requests or GitHub Issues. Propose first, patch second, both by email to [contact@podomy.com](mailto:contact@podomy.com): an RFC describing the problem, then a `git format-patch` series.

## License

Apache License 2.0. See [LICENSE](/LICENSE).
