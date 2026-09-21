"""Upload a local video file as a VOD asset."""

import os
import sys

from livepeer_frameworks import FrameWorksClient, UploadError, upload_vod


def main(path: str) -> None:
    with FrameWorksClient(token=os.environ["FRAMEWORKS_API_TOKEN"]) as fw:
        try:
            asset = upload_vod(
                fw,
                path,
                filename=os.path.basename(path),
                title="Launch recording",
                concurrency=4,
                on_progress=lambda done, total: print(f"{done}/{total} bytes"),
            )
        except UploadError as err:
            print(f"upload {err.upload_id} failed at part {err.part_number}: {err}")
            return
        print(asset.id, asset.playback_id, asset.status.value)


if __name__ == "__main__":
    main(sys.argv[1])
