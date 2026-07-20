# Vendored packages

`cuelume-0.1.2-14a71236.tgz` is built from
[`robo-monk/cuelume@14a71236ecaefeee80cc43cf52e049779f55d041`](https://github.com/robo-monk/cuelume/commit/14a71236ecaefeee80cc43cf52e049779f55d041).

The Git dependency is vendored because that revision does not commit `dist`
and defines `prepack` instead of the `prepare` lifecycle npm runs for Git
dependencies. Installing it with the GitHub shorthand therefore produces a
package without its declared JavaScript and type entry points.
