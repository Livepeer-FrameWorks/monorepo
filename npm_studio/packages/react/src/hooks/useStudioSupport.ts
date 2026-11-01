import { useEffect, useState } from "react";
import {
  detectStudioSupport,
  FULL_STUDIO_SUPPORT,
  type StudioSupport,
} from "@livepeer-frameworks/streamcrafter-core";

/**
 * What this browser lets a broadcaster do. The first render assumes everything,
 * so it matches server-rendered markup; the browser's answer follows on mount.
 */
export function useStudioSupport(): StudioSupport {
  const [support, setSupport] = useState(FULL_STUDIO_SUPPORT);

  useEffect(() => {
    setSupport(detectStudioSupport());
  }, []);

  return support;
}
