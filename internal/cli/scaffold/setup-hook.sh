#!/bin/sh
# Setup step, referenced from loop.yaml (steps.setup). Runs inside the fresh
# workdir after checkout, before the agent starts.
# Environment: LOOP_PROJECT_DIR, LOOP_WORKDIR, LOOP_BRANCH, LOOP_BASE,
# LOOP_ITEM_ID, LOOP_ITEM_TITLE, LOOP_RUN_ID, LOOP_RUN_DIR, LOOP_SUMMARY_FILE.
set -e
# cp "$HOME/.secrets/my-service.env" .env
