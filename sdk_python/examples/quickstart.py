"""Create a stream, then list every stream of the account."""

import os
from typing import Any

from livepeer_frameworks import FrameWorksClient, ResultError, expect_result, paginate_relay
from livepeer_frameworks.graphql import ConnectionInput, CreateStreamInput, Stream


def main() -> None:
    with FrameWorksClient(token=os.environ["FRAMEWORKS_API_TOKEN"]) as fw:
        created = fw.create_stream(input=CreateStreamInput(name="Studio A", record=True))
        try:
            stream = expect_result(created.create_stream, Stream)
        except ResultError as err:
            print(f"not created: {err.typename} {err.message} (field {err.field})")
            return
        print(stream.id, stream.stream_key, stream.playback_id)

        def page(request: dict[str, Any]) -> Any:
            return fw.list_streams(page=ConnectionInput.model_validate(request)).streams_connection

        for s in paginate_relay(page, page_size=50):
            print(s.name, s.metrics.is_live if s.metrics else False)


if __name__ == "__main__":
    main()
