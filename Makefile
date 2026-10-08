.PHONY: help structure

help:
	@echo "CosmicForge Real Estate"
	@echo "  make structure  Show top-level architecture directories"

structure:
	@find apps gateway services integrations shared contracts infrastructure firebase scripts tests docs -maxdepth 2 -type d 2>/dev/null | sort
