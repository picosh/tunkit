module github.com/picosh/tunkit

go 1.24

toolchain go1.24.0

replace github.com/picosh/pico => ../pico

require golang.org/x/crypto v0.36.0

require github.com/picosh/pico v0.0.0-00010101000000-000000000000

require (
	github.com/antoniomika/syncmap v1.0.0 // indirect
	golang.org/x/sys v0.31.0 // indirect
)
