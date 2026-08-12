.PHONY: test validate package

test:
	./tests/run.sh

validate:
	./install.sh --validate

package: test
	./scripts/package.sh
