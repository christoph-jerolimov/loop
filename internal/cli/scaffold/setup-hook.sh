#!/bin/sh
# Project setup hook. Runs in the fresh workdir after checkout, before the
# agent starts. Environment: LOOP_WORKDIR, LOOP_BRANCH, LOOP_BASE,
# LOOP_ITEM_ID, LOOP_ITEM_TITLE, LOOP_RUN_ID, LOOP_RUN_DIR, LOOP_SUMMARY_FILE.
# Global hooks live in ~/.config/loop/hooks/setup.d/ and run first.
set -e
# cp "$HOME/.secrets/my-service.env" .env
