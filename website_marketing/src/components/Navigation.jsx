import { Link, useLocation } from "react-router";
import { useEffect, useId, useRef, useState } from "react";
import config from "../config";
import { ArrowTopRightOnSquareIcon, ChevronDownIcon } from "@heroicons/react/24/outline";
import BetaBadge from "./shared/BetaBadge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { headerNav, isActiveLink, signupUrl } from "@/lib/siteLinks";
import { Sheet, SheetTrigger, SheetContent, SheetClose, SheetTitle } from "@/components/ui/sheet";

const ExternalIcon = ({ className }) => (
  <ArrowTopRightOnSquareIcon
    aria-hidden="true"
    className={cn(
      "shrink-0 transition-transform duration-200 group-hover:translate-x-1 group-hover:-translate-y-1 group-focus-visible:translate-x-1 group-focus-visible:-translate-y-1",
      className
    )}
  />
);

// Renders one link from the shared list: a router Link for site routes, an
// anchor for the docs site, and a new-tab anchor for external sites.
const NavLink = ({ link, className, children, ...props }) => {
  if (link.to) {
    return (
      <Link to={link.to} className={className} {...props}>
        {children ?? link.label}
      </Link>
    );
  }
  return (
    <a
      href={link.href}
      className={cn(link.external && "group", className)}
      {...(link.external ? { target: "_blank", rel: "noopener noreferrer" } : {})}
      {...props}
    >
      {children ?? link.label}
      {link.external ? <ExternalIcon className="ml-1 h-3 w-3" /> : null}
    </a>
  );
};

// Disclosure menu for the Resources group: opens on click or hover, closes on
// Escape, an outside click, or focus leaving the menu. The header keys it by
// path, so navigating remounts it closed.
const ResourcesMenu = ({ menu, active, pathname, linkClasses }) => {
  const [open, setOpen] = useState(false);
  const rootRef = useRef(null);
  const buttonRef = useRef(null);
  const closeTimer = useRef(null);
  const panelId = useId();

  useEffect(() => {
    if (!open) return undefined;
    const onPointerDown = (event) => {
      if (!rootRef.current?.contains(event.target)) setOpen(false);
    };
    const onKeyDown = (event) => {
      if (event.key === "Escape") {
        setOpen(false);
        buttonRef.current?.focus();
      }
    };
    document.addEventListener("pointerdown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  useEffect(() => () => window.clearTimeout(closeTimer.current), []);

  const openNow = () => {
    window.clearTimeout(closeTimer.current);
    setOpen(true);
  };
  const closeSoon = () => {
    window.clearTimeout(closeTimer.current);
    closeTimer.current = window.setTimeout(() => setOpen(false), 120);
  };

  return (
    <div
      ref={rootRef}
      className="nav-menu"
      onPointerEnter={(event) => event.pointerType === "mouse" && openNow()}
      onPointerLeave={(event) => event.pointerType === "mouse" && closeSoon()}
      onBlur={(event) => {
        if (!rootRef.current?.contains(event.relatedTarget)) setOpen(false);
      }}
    >
      <button
        ref={buttonRef}
        type="button"
        className={cn(linkClasses(active), "gap-1")}
        aria-expanded={open}
        aria-controls={panelId}
        onClick={() => setOpen((value) => !value)}
      >
        {menu.label}
        <ChevronDownIcon
          aria-hidden="true"
          className={cn("h-3.5 w-3.5 transition-transform duration-200", open && "rotate-180")}
        />
      </button>
      <div id={panelId} className="nav-menu__panel" hidden={!open}>
        <ul className="nav-menu__list">
          {menu.children.map((child) => (
            <li key={child.id}>
              <NavLink
                link={child}
                className={cn("nav-menu__item", isActiveLink(child, pathname) && "is-active")}
                aria-current={isActiveLink(child, pathname) ? "page" : undefined}
              >
                <span className="nav-menu__label">{child.label}</span>
                {child.description ? (
                  <span className="nav-menu__description">{child.description}</span>
                ) : null}
              </NavLink>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
};

const Navigation = () => {
  const location = useLocation();
  const [isMenuOpen, setIsMenuOpen] = useState(false);
  const items = headerNav(config);
  const tryNowHref = signupUrl(config.appUrl, "nav-try-now");

  const linkClasses = (isActive, variant = "desktop") =>
    cn(
      "text-sm font-medium transition-colors duration-200",
      variant === "desktop" ? "nav-link inline-flex items-center" : "nav-mobile-link",
      isActive
        ? variant === "desktop"
          ? "nav-link-active text-accent"
          : "nav-mobile-link-active"
        : "text-muted-foreground hover:text-accent"
    );

  useEffect(() => {
    if (isMenuOpen) {
      setIsMenuOpen(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location.pathname]);

  return (
    <nav className="nav-surface fixed top-0 left-0 right-0 z-50" aria-label="Primary">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="flex justify-between items-center h-16">
          <div className="flex items-center gap-3">
            <Link to="/" className="flex items-center">
              <img
                src="/frameworks-dark-horizontal-lockup-transparent.svg"
                alt={config.companyName}
                width="186"
                height="40"
                className="h-10"
              />
            </Link>
            <BetaBadge />
          </div>

          {/* Desktop navigation: shown from xl, where every item fits on one line. */}
          <div className="hidden xl:flex items-center gap-6">
            {items.map((item) =>
              item.children ? (
                <ResourcesMenu
                  key={`${item.id}:${location.pathname}`}
                  menu={item}
                  active={isActiveLink(item, location.pathname)}
                  pathname={location.pathname}
                  linkClasses={(active) => linkClasses(active)}
                />
              ) : (
                <NavLink
                  key={item.id}
                  link={item}
                  className={linkClasses(isActiveLink(item, location.pathname))}
                  aria-current={isActiveLink(item, location.pathname) ? "page" : undefined}
                />
              )
            )}
            <Button asChild className="cta-button">
              <a href={tryNowHref} className="flex items-center gap-2">
                Try Now
                <ArrowTopRightOnSquareIcon className="w-4 h-4 cta-button__icon" />
              </a>
            </Button>
          </div>

          <Sheet open={isMenuOpen} onOpenChange={setIsMenuOpen}>
            <SheetTrigger asChild>
              <button
                className="nav-trigger p-2 xl:hidden"
                aria-label={isMenuOpen ? "Close navigation" : "Open navigation"}
                aria-expanded={isMenuOpen}
                aria-controls="mobile-nav-panel"
              >
                <svg
                  className="w-6 h-6"
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                  aria-hidden="true"
                >
                  {isMenuOpen ? (
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M6 18L18 6M6 6l12 12"
                    />
                  ) : (
                    <path
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={2}
                      d="M4 6h16M4 12h16M4 18h16"
                    />
                  )}
                </svg>
              </button>
            </SheetTrigger>
            <SheetContent
              side="top"
              id="mobile-nav-panel"
              className="nav-mobile-surface max-h-[100dvh] overflow-y-auto px-4 pb-8 pt-6 shadow-lg xl:hidden"
            >
              <SheetTitle className="sr-only">Primary navigation</SheetTitle>
              <div className="flex flex-col gap-3 text-left">
                {items.map((item) =>
                  item.children ? (
                    <div key={item.id} className="nav-divider mt-3 pt-3 border-t">
                      <p className="nav-group-label mb-2">{item.label}</p>
                      <div className="flex flex-col gap-3">
                        {item.children.map((child) => (
                          <SheetClose asChild key={child.id}>
                            <NavLink
                              link={child}
                              className={linkClasses(
                                isActiveLink(child, location.pathname),
                                "mobile"
                              )}
                            />
                          </SheetClose>
                        ))}
                      </div>
                    </div>
                  ) : (
                    <SheetClose asChild key={item.id}>
                      <NavLink
                        link={item}
                        className={cn(
                          linkClasses(isActiveLink(item, location.pathname), "mobile"),
                          item.external && "w-full gap-2"
                        )}
                      />
                    </SheetClose>
                  )
                )}

                <SheetClose asChild>
                  <Button asChild className="cta-button w-full justify-center mt-2">
                    <a href={tryNowHref} className="flex items-center gap-2">
                      Try Now
                      <ArrowTopRightOnSquareIcon className="w-4 h-4 cta-button__icon" />
                    </a>
                  </Button>
                </SheetClose>
              </div>
            </SheetContent>
          </Sheet>
        </div>
      </div>
    </nav>
  );
};

export default Navigation;
