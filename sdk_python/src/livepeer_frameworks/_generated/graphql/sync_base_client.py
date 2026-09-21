"""Base class of the generated sync client.

ariadne-codegen copies this file into the generated package. The transport
itself (auth, retries, the serverInfo gate, typed errors) lives in
livepeer_frameworks._transport.
"""

from livepeer_frameworks._transport import SyncTransport


class BaseClient(SyncTransport):
    """Runs the generated sync operations through the SDK transport."""
