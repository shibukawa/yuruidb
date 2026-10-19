#!/usr/bin/env python3
"""Compile actual tagged import graphs and check the registered driver sets."""
import os
import subprocess

cases = {
    "": set(),
    "aws": {"s3", "dynamodb"},
    "gcp": {"gcs", "firebase", "firestore"},
    "cloudflare": {"r2", "d1", "cfkv"},
    "aws,s3,dynamodb": {"s3", "dynamodb"},
    "aws,gcp,cloudflare": {"s3", "dynamodb", "gcs", "firebase", "firestore", "r2", "d1", "cfkv"},
    "lambda,s3": {"s3"},
    "gcs,r2": {"gcs", "r2"},
    "memory,bbolt,sql": {"memory", "bbolt", "sql"},
}
for tags, expected in cases.items():
    result = subprocess.run(["go", "run", "-tags", tags, "./examples/drivers"], check=True, capture_output=True, text=True)
    actual = set(filter(None, result.stdout.strip().split(",")))
    assert actual == expected, (tags, expected, actual)
    print(f"{tags or '(none)'}: {','.join(sorted(actual)) or '(none)'}")
root_deps = subprocess.run(["go", "list", "-deps", "."], capture_output=True, text=True, check=True).stdout.splitlines()
assert not any("/drivers/" in dep or "go.etcd.io" in dep or "aws-sdk" in dep or "cloud.google.com" in dep for dep in root_deps), root_deps
for tags in ("aws", "gcp", "cloudflare", "aws,gcp,cloudflare"):
    env = dict(os.environ, GOOS="linux", GOARCH="arm64", CGO_ENABLED="0")
    subprocess.run(["go", "build", "-tags", tags, "-o", os.devnull, "./examples/drivers"], env=env, check=True)
print("core dependency isolation and Linux/arm64 cloud-profile builds: passed")
