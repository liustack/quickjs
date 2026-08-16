[![Go Reference](https://pkg.go.dev/badge/modernc.org/quickjs.svg)](https://pkg.go.dev/modernc.org/quickjs)
[![LiberaPay](https://liberapay.com/assets/widgets/donate.svg)](https://liberapay.com/jnml/donate)
[![receives](https://img.shields.io/liberapay/receives/jnml.svg?logo=liberapay)](https://liberapay.com/jnml/donate)
[![patrons](https://img.shields.io/liberapay/patrons/jnml.svg?logo=liberapay)](https://liberapay.com/jnml/donate)

![logo_png](logo.png)

# quickjs

Package quickjs is a pure Go embeddable Javascript engine. It supports the
ECMA script 14 ([ES2023]) specification including modules, asynchronous
generators, proxies and BigInt.

See also the original [C QuickJS] library.

The engine has no `Intl`: ECMA-402 is not implemented by the C library this
package wraps. Package [quickjs/intl] adds `Intl.DateTimeFormat` and
`Intl.NumberFormat`, en-US over Go's time zone database, for a VM that opts in
by calling its `Register`.

[C QuickJS]: https://bellard.org/quickjs
[quickjs/intl]: https://pkg.go.dev/modernc.org/quickjs/intl
[ES2023]: https://tc39.es/ecma262/2023/
