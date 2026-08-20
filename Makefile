RELEASE = cargo +nightly build --release --config 'build.rustflags="-C force-unwind-tables=no"'

.PHONY: check run dev harness

check:
	cargo fmt
	cargo clippy --all-targets -- -D warnings
	cargo test
	$(RELEASE)
	python3 scripts/harness.py --bin target/release/ax

run:
	$(RELEASE)
	./target/release/ax

dev:
	cargo build
	./target/debug/ax

harness:
	$(RELEASE)
	python3 scripts/harness.py --bin target/release/ax
