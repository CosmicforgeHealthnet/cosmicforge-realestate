.PHONY: help tree

help:
	@echo "CosmicForge Real Estate monorepo"
	@echo "  make tree   Show top-level repository structure"

tree:
	@find . -maxdepth 3 -type d | sort
