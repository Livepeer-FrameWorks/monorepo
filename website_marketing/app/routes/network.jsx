import { useEffect, useRef } from "react";
import config from "../../src/config";
import "maplibre-gl/dist/maplibre-gl.css";

export function meta() {
  return [
    { title: "Livepeer Network | FrameWorks" },
    {
      name: "description",
      content:
        "Public Livepeer endpoints, discovery coverage, regional transcoding performance and pricing.",
    },
  ];
}
export default function NetworkPage() {
  const container = useRef(null);
  useEffect(() => {
    let disposed = false;
    let stop;
    import("../../../packages/map-core/network-dashboard").then(({ mountNetworkDashboard }) => {
      if (!disposed && container.current) {
        const api = new URL("/v1/network/livepeer", config.gatewayUrl).toString();
        stop = mountNetworkDashboard(container.current, {
          apiURL: api,
          infrastructureURL: "/status",
          graphqlURL: config.gatewayUrl,
        });
      }
    });
    return () => {
      disposed = true;
      stop?.();
    };
  }, []);
  return (
    <div className="pt-24 pb-12 px-4">
      <h1 className="text-3xl font-semibold mb-6">The network</h1>
      <div ref={container} />
    </div>
  );
}
