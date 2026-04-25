from __future__ import annotations

import argparse

from .app import KontextApp


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="kontext",
        description="Launch and watch Kubernetes-native Agent resources.",
    )
    subparsers = parser.add_subparsers(dest="command")
    demo = subparsers.add_parser("demo", help="Open the guided terminal UI")
    demo.add_argument(
        "--fallback",
        action="store_true",
        help="Start in canned demo mode instead of reading from Kubernetes.",
    )
    return parser


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.command in {None, "demo"}:
        KontextApp(fallback=getattr(args, "fallback", False)).run()
        return 0

    parser.print_help()
    return 2
