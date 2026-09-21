"""Sign a viewer playback token and resolve a playback endpoint with it."""

import os

from livepeer_frameworks import FrameWorksClient, sign_playback_token


def main() -> None:
    token = sign_playback_token(
        os.environ["FRAMEWORKS_SIGNING_PRIVATE_KEY"],
        os.environ["FRAMEWORKS_SIGNING_KID"],
        subject="viewer-42",
        audience="viewer",
        expires_in=300,
        claims={"tier": "pro"},
    )
    with FrameWorksClient("https://bridge.frameworks.network/graphql") as fw:
        resolved = fw.resolve_viewer_endpoint(content_id="PLAYBACK_ID", playback_token=token)
        endpoint = resolved.resolve_viewer_endpoint
        if endpoint and endpoint.primary:
            print(f"{endpoint.primary.url}?jwt={token}")


if __name__ == "__main__":
    main()
