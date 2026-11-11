# frozen_string_literal: true

# Copyright (c) 2025 kk
#
# This software is released under the MIT License.
# https://opensource.org/licenses/MIT
#
#   rake push                 # commit + push + semver tag (kk-git)
#   SKIP_TAG=1 rake push      # commit + push only
#   KK_GIT_DRY_RUN=1 rake push
#   KK_GIT_BUMP=minor rake push

begin
  require 'bundler/setup'
rescue LoadError
  # kk-git installed globally
end
require 'kk/git/rake_tasks'

task default: %w[push]

desc 'Commit + push, then bump and push a new v* tag (triggers GoReleaser)'
task :push do
  # kk-git defaults to `git pull --ff-only`, which fails on diverged branches.
  ENV['KK_GIT_PULL_ARGS'] ||= '--no-rebase'
  Rake::Task['git:auto_commit_push_tag'].invoke
end
