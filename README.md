# sidecore

![Sidecore](art/sidecorelogo.png)

Using flattened Docker images from <https://github.com/u-root/sidecore-images>,
**sidecore** lets you run IoT systems as easily as you run a shell script.

## How it works

Building on top of [`cpu`](https://github.com/u-root/cpu), **sidecore** merely
requires your IoT system to run a small daemon in order to use it as a resource.

## Building

**NOTE**: Go version 1.20 is required as a minimum.

Run `go build ./cmds/sidecore` to build the command.

## Usage

After building the command, run `./sidecore -h` for help.
