RELEASE = cargo +nightly build --release --config 'build.rustflags="-C force-unwind-tables=no"'

.PHONY: check run dev

check:
	cargo fmt
	cargo clippy --all-targets -- -D warnings
	cargo test
	$(RELEASE)

run:
	$(RELEASE)
	./target/release/ax

dev:
	cargo build
	./target/debug/ax
