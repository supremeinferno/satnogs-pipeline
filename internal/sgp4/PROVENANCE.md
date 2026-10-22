# Provenance

`sgp4.go`, `deepspace.go`, `sgp4_test.go`, `testdata/` here and `../tle/` are copied unchanged, except for the import path, from
[supremeinferno/orbitscreen](https://github.com/supremeinferno/orbitscreen) (`internal/sgp4`, `internal/tle`), a Go port of David Vallado's reference SGP4 verified against his test set.
They are copied rather than imported because `internal/` packages cannot be imported from another module. The next step is to move them to a shared, importable package and delete this copy.
