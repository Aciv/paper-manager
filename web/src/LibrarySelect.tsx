import { useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react';
import { createPortal } from 'react-dom';

import './LibrarySelect.css';

export type LibrarySelectOption = { value: string; label: string };

type MenuPlacement = {
  left: number;
  width: number;
  top?: number;
  bottom?: number;
  maxHeight: number;
};

// Native <select> popups size themselves to their content and escape the
// control's width, which clips long venue names at the column edge. This keeps
// the option list exactly as wide as the trigger, wraps long labels, and lets
// the list scroll vertically. The menu is portalled so the narrow side column
// cannot clip it.
export function LibrarySelect({ value, options, onChange, ariaLabel }: {
  value: string;
  options: LibrarySelectOption[];
  onChange(value: string): void;
  ariaLabel: string;
}) {
  const [open, setOpen] = useState(false);
  const [placement, setPlacement] = useState<MenuPlacement>();
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const selected = options.find((option) => option.value === value) ?? options[0];

  function placeMenu() {
    const rect = triggerRef.current?.getBoundingClientRect();
    if (!rect) return;
    const margin = 8;
    const gap = 4;
    const spaceBelow = window.innerHeight - rect.bottom - gap - margin;
    const spaceAbove = rect.top - gap - margin;
    // Open upward only when the control is near the bottom and there is more
    // room above it.
    const openAbove = spaceBelow < 140 && spaceAbove > spaceBelow;
    const maxHeight = Math.max(96, Math.min(260, openAbove ? spaceAbove : spaceBelow));
    setPlacement({
      left: rect.left,
      width: rect.width,
      maxHeight,
      ...(openAbove
        ? { bottom: window.innerHeight - rect.top + gap }
        : { top: rect.bottom + gap }),
    });
  }

  function openMenu() {
    placeMenu();
    setOpen(true);
  }

  function closeMenu(restoreFocus = false) {
    setOpen(false);
    if (restoreFocus) window.requestAnimationFrame(() => triggerRef.current?.focus());
  }

  function revealOption(option: HTMLElement) {
    const menu = menuRef.current;
    if (!menu) return;
    const top = option.offsetTop;
    if (top < menu.scrollTop) menu.scrollTop = Math.max(0, top - 4);
    else if (top + option.offsetHeight > menu.scrollTop + menu.clientHeight) {
      menu.scrollTop = top + option.offsetHeight - menu.clientHeight + 4;
    }
  }

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (triggerRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      closeMenu(true);
    };
    const onScroll = (event: Event) => {
      // Scrolling the option list itself is expected; any ancestor moving means
      // the fixed menu would detach from its trigger.
      if (event.target instanceof Node && menuRef.current?.contains(event.target)) return;
      setOpen(false);
    };
    const onResize = () => setOpen(false);
    document.addEventListener('pointerdown', onPointerDown, true);
    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true);
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onResize);
    };
  }, [open]);

  // Options stay one line high: an overflowing label is a horizontal sliding
  // window, driven by the wheel. The native listener is required because React's
  // wheel handler is passive and cannot preventDefault.
  useEffect(() => {
    if (!open) return;
    const menu = menuRef.current;
    if (!menu) return;
    const onWheel = (event: WheelEvent) => {
      const option = (event.target as HTMLElement | null)?.closest<HTMLElement>('.library-select-option');
      if (!option || option.scrollWidth <= option.clientWidth + 1) return;
      const delta = Math.abs(event.deltaY) >= Math.abs(event.deltaX) ? event.deltaY : event.deltaX;
      if (!delta) return;
      event.preventDefault();
      option.scrollLeft += delta * (event.deltaMode === 1 ? 16 : 1);
    };
    menu.addEventListener('wheel', onWheel, { passive: false });
    return () => menu.removeEventListener('wheel', onWheel);
  }, [open]);

  // Focus the current choice when the menu opens without scrolling the whole
  // page (the fixed menu can scroll itself).
  useLayoutEffect(() => {
    if (!open) return;
    const menu = menuRef.current;
    if (!menu) return;
    const option = menu.querySelector<HTMLButtonElement>('[aria-selected="true"]')
      ?? menu.querySelector<HTMLButtonElement>('button');
    option?.focus({ preventScroll: true });
    if (option) revealOption(option);
  }, [open]);

  function handleMenuKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const buttons = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('button[role="option"]') ?? []);
    if (buttons.length === 0) return;
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    let next = 0;
    if (event.key === 'ArrowDown') next = current < 0 ? 0 : Math.min(buttons.length - 1, current + 1);
    else if (event.key === 'ArrowUp') next = current <= 0 ? 0 : current - 1;
    else if (event.key === 'End') next = buttons.length - 1;
    buttons[next]?.focus({ preventScroll: true });
    revealOption(buttons[next]);
  }

  const label = selected?.label ?? '';
  return <>
    <button
      ref={triggerRef}
      className={`library-select-trigger${open ? ' open' : ''}`}
      type="button"
      aria-haspopup="listbox"
      aria-expanded={open}
      aria-label={ariaLabel}
      title={label}
      onClick={() => (open ? closeMenu() : openMenu())}
      onKeyDown={(event) => {
        if (event.key === 'ArrowDown' || event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          if (open) closeMenu();
          else openMenu();
        }
      }}>
      <span>{label}</span>
      <svg viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 6 8 10.5 12.5 6" /></svg>
    </button>
    {open && placement && createPortal(
      <div
        ref={menuRef}
        className="library-select-menu"
        role="listbox"
        aria-label={ariaLabel}
        style={{
          left: placement.left,
          top: placement.top,
          bottom: placement.bottom,
          width: placement.width,
          maxHeight: placement.maxHeight,
        }}
        onKeyDown={handleMenuKeyDown}>
        {options.map((option) => <button
          key={option.value || '__all'}
          className={`library-select-option${option.value === value ? ' active' : ''}`}
          type="button"
          role="option"
          aria-selected={option.value === value}
          title={option.label}
          onClick={() => { onChange(option.value); closeMenu(true); }}>
          {option.label}
        </button>)}
      </div>,
      document.body,
    )}
  </>;
}
