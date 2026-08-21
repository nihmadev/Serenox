"""Command-line entry point for the supervised Python sidecar."""

from __future__ import annotations

import argparse
import asyncio
import contextlib
import os
import signal
from collections.abc import Sequence

from serenox_sdk.protocol import PROTOCOL_VERSION

from .host import HostSettings, SidecarHost, load_plugin_module


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Run one isolated Serenox Python plugin")
    parser.add_argument(
        "--socket",
        default=os.environ.get("SERENOX_PLUGIN_SOCKET", os.environ.get("SERENOX_SOCKET")),
        help="daemon endpoint: Unix path/unix://path or portable tcp://host:port",
    )
    parser.add_argument(
        "--token",
        default=os.environ.get("SERENOX_PLUGIN_TOKEN", os.environ.get("SERENOX_TOKEN")),
        help=argparse.SUPPRESS,
    )
    parser.add_argument(
        "--instance-id",
        default=os.environ.get("SERENOX_PLUGIN_INSTANCE_ID"),
    )
    parser.add_argument("--module", default=os.environ.get("SERENOX_PYTHON_MODULE"))
    parser.add_argument(
        "--class", dest="class_name", default=os.environ.get("SERENOX_PYTHON_CLASS")
    )
    parser.add_argument("--queue-capacity", type=int, default=64)
    parser.add_argument("--workers", type=int, default=2)
    parser.add_argument("--callback-capacity", type=int, default=256)
    parser.add_argument(
        "--max-frame-bytes",
        type=int,
        default=os.environ.get("SERENOX_PLUGIN_MAX_FRAME_BYTES", str(1 << 20)),
    )
    return parser


async def _run(arguments: argparse.Namespace) -> None:
    missing = [
        name
        for name, value in {
            "--socket": arguments.socket,
            "--token": arguments.token,
            "--instance-id": arguments.instance_id,
            "--module": arguments.module,
        }.items()
        if not value
    ]
    if missing:
        raise SystemExit(f"missing required sidecar argument(s): {', '.join(missing)}")
    requested_protocol = os.environ.get("SERENOX_PLUGIN_PROTOCOL", PROTOCOL_VERSION)
    if requested_protocol != PROTOCOL_VERSION:
        raise SystemExit(
            f"SERENOX_PLUGIN_PROTOCOL={requested_protocol!r} is incompatible; "
            f"this sidecar implements {PROTOCOL_VERSION!r}"
        )
    module = load_plugin_module(str(arguments.module), arguments.class_name)
    host = SidecarHost(
        module,
        auth_token=str(arguments.token),
        instance_id=str(arguments.instance_id),
        settings=HostSettings(
            queue_capacity=arguments.queue_capacity,
            worker_count=arguments.workers,
            callback_capacity=arguments.callback_capacity,
            max_frame_bytes=arguments.max_frame_bytes,
        ),
    )
    current = asyncio.current_task()
    loop = asyncio.get_running_loop()
    if current is not None:
        for signum in (signal.SIGINT, signal.SIGTERM):
            with contextlib.suppress(NotImplementedError, RuntimeError):
                loop.add_signal_handler(signum, current.cancel)
    try:
        await host.connect_and_serve(str(arguments.socket))
    except asyncio.CancelledError:
        return
    finally:
        for signum in (signal.SIGINT, signal.SIGTERM):
            with contextlib.suppress(NotImplementedError, RuntimeError):
                loop.remove_signal_handler(signum)


def main(argv: Sequence[str] | None = None) -> None:
    arguments = _parser().parse_args(argv)
    asyncio.run(_run(arguments))


if __name__ == "__main__":
    main()
