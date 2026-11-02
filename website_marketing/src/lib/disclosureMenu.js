// Open state of the header's hover-and-click disclosure menus. A mouse opens
// the menu by hovering, so a mouse click on the trigger must keep it open
// rather than toggle it shut. Keyboard and touch have no hover and toggle.
export function nextMenuOpen(open, action) {
  switch (action.type) {
    case "hover":
      return action.pointerType === "mouse" ? true : open;
    case "leave":
      return action.pointerType === "mouse" ? false : open;
    case "click":
      return action.pointerType === "mouse" ? true : !open;
    case "dismiss":
      return false;
    default:
      return open;
  }
}
