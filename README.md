# faas

Function as a Service CLI

## Setup and Configuration

With Go 1.13+ installed, build and install the binary to your path:
```
go install
```

Install dependent binaries:

* `kn`  https://github.com/knative/client/releases
* `kubectl` https://kubernetes.io/docs/tasks/tools/install-kubectl/
* `docker` https://docs.docker.com/get-docker/

Configure Image repository:

Both the image repository and user/org namespace need to be defined either by
using the --registry and --namespace flags on the `create` command, or by
configuring as environment variables.  For example to configure all images
to be pushed to `quay.io/alice`, use:
```
export FAAS_REGISTRY=quay.io
export FAAS_NAMESPACE=alice
```

Cluster connection:

It is expected that kubectl and kn be configured to connect to a kubernetes cluster with the following configuration:

* Knative Serving and Eventing
* Knative Domains patched to enable your chosen domain
* Knative Network patched to enable subdomains
* Kourier
* Cert-manager

see https://github.com/lkingland/config for cluster setup and configuration details.

## Usage

See help:
```shell
faas
```
## Examples

Create a new Service Function:

```shell
> mkdir -p example.com/www
> cd example.com/www
> faas create go
https://www.example.com
> curl https://www.example.com
OK
```


