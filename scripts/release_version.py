#!/usr/bin/env python3
"""Release tag -> numeric plugin ABI version; branches/PRs use unreleased 0.0.0."""
import argparse
import re

TAG = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*))?")

def parse_tag(tag):
    match = TAG.fullmatch(tag)
    if not match:
        raise ValueError("tag must be vX.Y.Z or vX.Y.Z-prerelease")
    return '.'.join(match.groups()[:3]), match[4] is not None

def build_version(ref, event):
    if ref.startswith('refs/tags/'):
        if event != 'push':
            raise ValueError('release builds require a tag push')
        return parse_tag(ref[len('refs/tags/'):])[0]
    return '0.0.0'

if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--ref', required=True)
    p.add_argument('--event', required=True)
    a = p.parse_args()
    try:
        print(build_version(a.ref, a.event))
    except ValueError as e:
        p.error(str(e))
