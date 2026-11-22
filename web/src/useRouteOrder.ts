import { useEffect, useRef, useState, type ButtonHTMLAttributes, type CSSProperties } from "react";
import { api, type Group, type Route } from "./api";

type RowBox = { id: number; top: number; bottom: number; height: number };
type Drag = {
  groupID: number;
  sourceID: number;
  targetID: number | null;
  rows: RowBox[];
  offsetY: number;
  released?: boolean;
};
type PointerDrag = Drag & {
  group: Group;
  pointerID: number;
  startX: number;
  startY: number;
  startScrollY: number;
  viewport: HTMLElement;
  element: HTMLTableRowElement;
  x: number;
  y: number;
  moved: boolean;
};

export default function useRouteOrder(refresh: () => Promise<void>) {
  const pointer = useRef<PointerDrag | null>(null);
  const saving = useRef(false);
  const scrollFrame = useRef<number | null>(null);
  const [drag, setDrag] = useState<Drag | null>(null);
  const [savingGroup, setSavingGroup] = useState<number | null>(null);
  const [message, setMessage] = useState<{ groupID: number; error: boolean; text: string } | null>(null);
  useEffect(() => () => {
    if (scrollFrame.current != null) cancelAnimationFrame(scrollFrame.current);
  }, []);

  async function move(group: Group, sourceID: number, targetID: number) {
    const from = group.routes.findIndex((route) => route.id === sourceID);
    const to = group.routes.findIndex((route) => route.id === targetID);
    if (saving.current || from < 0 || to < 0 || from === to) return;
    const routes = [...group.routes];
    routes.splice(to, 0, routes.splice(from, 1)[0]);
    saving.current = true;
    setSavingGroup(group.id);
    setMessage(null);
    try {
      await api(`/groups/${group.id}/order`, "PATCH", {
        strategy: group.strategy,
        route_ids: routes.map((route) => route.id),
        expected_routes: group.routes.map(({ id, priority, weight }) => ({ id, priority, weight })),
      });
      try {
        await refresh();
        setMessage({ groupID: group.id, error: false, text: "顺序已保存" });
      } catch {
        setMessage({ groupID: group.id, error: true, text: "顺序已保存，页面刷新失败，请点击刷新查看最新顺序。" });
      }
    } catch (error) {
      setMessage({ groupID: group.id, error: true, text: (error as Error).message });
    } finally {
      saving.current = false;
      setSavingGroup(null);
      setDrag(null);
    }
  }

  function targetAt(session: PointerDrag, x: number, y: number) {
    const viewport = session.viewport.getBoundingClientRect();
    const rows = session.rows;
    if (x < viewport.left || x > viewport.right || y < viewport.top || y > viewport.bottom || y + scrollY < rows[0].top || y + scrollY > rows[rows.length - 1].bottom) return null;
    let index = rows.findIndex((row) => row.id === session.sourceID);
    const center = (rows[index].top + rows[index].bottom) / 2 + y - session.startY + scrollY - session.startScrollY;
    // 触摸坐标会按像素取整，中心点比较保留 1px 容差。
    while (index + 1 < rows.length && center >= (rows[index + 1].top + rows[index + 1].bottom) / 2 - 1) index++;
    while (index > 0 && center <= (rows[index - 1].top + rows[index - 1].bottom) / 2 + 1) index--;
    return rows[index].id;
  }

  function releasePointer() {
    pointer.current = null;
    if (scrollFrame.current != null) cancelAnimationFrame(scrollFrame.current);
    scrollFrame.current = null;
  }

  function cancel() {
    releasePointer();
    setDrag(null);
  }

  function updateDrag(session: PointerDrag) {
    const source = session.rows.find((row) => row.id === session.sourceID)!;
    session.offsetY = Math.max(
      session.rows[0].top - source.top,
      Math.min(session.rows[session.rows.length - 1].bottom - source.bottom,
        session.y - session.startY + scrollY - session.startScrollY),
    );
    session.targetID = targetAt(session, session.x, session.y);
    setDrag((previous) => previous?.offsetY === session.offsetY && previous.targetID === session.targetID ? previous : { ...session });
  }

  function scrollWhileDragging() {
    const session = pointer.current;
    if (!session?.moved) return;
    if (!session.element.isConnected) {
      cancel();
      return;
    }
    const distance = session.y < 80 ? session.y - 80 : session.y > innerHeight - 80 ? session.y - innerHeight + 80 : 0;
    if (distance) {
      window.scrollBy(0, Math.max(-18, Math.min(18, distance / 4)));
    }
    updateDrag(session);
    scrollFrame.current = requestAnimationFrame(scrollWhileDragging);
  }

  function handleProps(group: Group, route: Route): ButtonHTMLAttributes<HTMLButtonElement> {
    return {
      type: "button",
      disabled: savingGroup !== null || group.routes.length < 2 || !route.id,
      title: "拖动调整顺序，或聚焦后使用上下方向键",
      "aria-describedby": `route-order-hint-${group.id}`,
      onPointerDown(event) {
        if (saving.current || event.button !== 0 || !event.isPrimary || !route.id) return;
        const row = event.currentTarget.closest<HTMLTableRowElement>("tr");
        const viewport = row?.closest<HTMLElement>(".table-scroll");
        if (!row?.parentElement || !viewport) return;
        const rows = Array.from(row.parentElement.querySelectorAll<HTMLTableRowElement>("tr[data-route-id]")).map((element) => {
          const box = element.getBoundingClientRect();
          return { id: Number(element.dataset.routeId), top: box.top + scrollY, bottom: box.bottom + scrollY, height: box.height };
        });
        if (rows.length !== group.routes.length) return;
        event.preventDefault();
        event.currentTarget.focus({ preventScroll: true });
        event.currentTarget.setPointerCapture(event.pointerId);
        pointer.current = {
          groupID: group.id, sourceID: route.id, targetID: route.id, group,
          pointerID: event.pointerId, startX: event.clientX, startY: event.clientY, moved: false,
          x: event.clientX, y: event.clientY,
          startScrollY: scrollY, viewport, element: row, rows, offsetY: 0,
        };
      },
      onPointerMove(event) {
        const session = pointer.current;
        if (!session || session.pointerID !== event.pointerId) return;
        session.x = event.clientX;
        session.y = event.clientY;
        session.moved ||= Math.hypot(event.clientX - session.startX, event.clientY - session.startY) > 5;
        if (!session.moved) return;
        if (scrollFrame.current == null) scrollFrame.current = requestAnimationFrame(scrollWhileDragging);
        updateDrag(session);
      },
      onPointerUp(event) {
        const session = pointer.current;
        if (!session || session.pointerID !== event.pointerId) return;
        const targetID = targetAt(session, event.clientX, event.clientY);
        if (!session.moved || targetID == null || targetID === session.sourceID) {
          cancel();
          return;
        }
        const source = session.rows.find((row) => row.id === session.sourceID)!;
        const target = session.rows.find((row) => row.id === targetID)!;
        releasePointer();
        setDrag({ ...session, targetID, released: true, offsetY: target.top < source.top ? target.top - source.top : target.bottom - source.bottom });
        void move(session.group, session.sourceID, targetID);
      },
      onPointerCancel: cancel,
      onLostPointerCapture() { if (pointer.current) cancel(); },
      onKeyDown(event) {
        if (event.key === "Escape") {
          cancel();
          return;
        }
        if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
        event.preventDefault();
        if (pointer.current || !route.id) return;
        const index = group.routes.findIndex((item) => item.id === route.id);
        const target = group.routes[index + (event.key === "ArrowUp" ? -1 : 1)];
        if (target?.id) void move(group, route.id, target.id);
      },
    };
  }

  function rowClass(group: Group, route: Route) {
    return drag?.groupID === group.id && drag.sourceID === route.id
      ? `route-dragging${drag.released ? " route-drag-settling" : ""}` : undefined;
  }

  function rowStyle(group: Group, route: Route): CSSProperties | undefined {
    if (!drag || drag.groupID !== group.id) return undefined;
    if (drag.sourceID === route.id) return { transform: `translate3d(0, ${drag.offsetY}px, 0)` };
    const from = drag.rows.findIndex((row) => row.id === drag.sourceID);
    const to = drag.rows.findIndex((row) => row.id === drag.targetID);
    const index = drag.rows.findIndex((row) => row.id === route.id);
    if (to < 0 || index < 0) return undefined;
    const shift = from < to && index > from && index <= to ? -drag.rows[from].height
      : from > to && index >= to && index < from ? drag.rows[from].height : 0;
    return { transform: `translate3d(0, ${shift}px, 0)` };
  }

  return { handleProps, rowClass, rowStyle, draggingGroup: drag?.groupID, savingGroup, message };
}
