import { ContextMenu as ContextMenuPrimitive } from "bits-ui";

import ContextMenuContent from "./ContextMenuContent.svelte";
import ContextMenuItem from "./ContextMenuItem.svelte";
import ContextMenuSeparator from "./ContextMenuSeparator.svelte";
import ContextMenuLabel from "./ContextMenuLabel.svelte";
import ContextMenuCheckboxItem from "./ContextMenuCheckboxItem.svelte";
import ContextMenuRadioItem from "./ContextMenuRadioItem.svelte";
import ContextMenuSubContent from "./ContextMenuSubContent.svelte";
import ContextMenuSubTrigger from "./ContextMenuSubTrigger.svelte";
import ContextMenuShortcut from "./ContextMenuShortcut.svelte";
import ContextMenuPortal from "./ContextMenuPortal.svelte";

const ContextMenu = ContextMenuPrimitive.Root;
const ContextMenuTrigger = ContextMenuPrimitive.Trigger;
const ContextMenuGroup = ContextMenuPrimitive.Group;
const ContextMenuSub = ContextMenuPrimitive.Sub;
const ContextMenuRadioGroup = ContextMenuPrimitive.RadioGroup;

export {
  ContextMenu,
  ContextMenuTrigger,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuCheckboxItem,
  ContextMenuRadioItem,
  ContextMenuLabel,
  ContextMenuSeparator,
  ContextMenuShortcut,
  ContextMenuGroup,
  ContextMenuPortal,
  ContextMenuSub,
  ContextMenuSubContent,
  ContextMenuSubTrigger,
  ContextMenuRadioGroup,
};
