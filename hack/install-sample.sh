#!/usr/bin/env bash


kubectl kustomize config/samples | kubectl apply --server-side --force-conflicts -f-
