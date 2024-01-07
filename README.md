# sidecore

![Sidecore](art/sidecorelogo.png)

Using flattened Docker images from <https://github.com/u-root/sidecore-images>,
**sidecore** lets you run IoT systems as easily as you run a shell script.

## How it works

Building on top of [`cpu`](https://github.com/u-root/cpu), **sidecore** merely
requires your IoT system to run a small daemon in order to use it as a resource.

## Build and run

**NOTE**: Go version 1.20 is required as a minimum.

### Git repository

If you have cloned this repository, run `go build ./cmds/sidecore` to build the
command, then `./sidecore -h` for help.

### Quick start

To try out the command without explicitly cloning, run:

```sh
go run github.com/u-root/sidecore/cmds/sidecore@latest
```

### Installation

To build the binary into your local Go bin directory, run:

```sh
go install github.com/u-root/sidecore/cmds/sidecore@latest
```

**NOTE**: Ensure to have that bin directory in your `$PATH` to run the resulting
binary conveniently. For more details, look at the `go install` section in the
[Go modules documentation](https://go.dev/ref/mod#go-install).
