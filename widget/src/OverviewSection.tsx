import type { ReactNode } from "react";
import type { SectionId } from "./reading";

export default function OverviewSection({ id, title, meta, collapsible, collapsed, onCollapse, children }: {
  id: SectionId; title: string; meta?: string; collapsible: boolean; collapsed: boolean;
  onCollapse: (id: SectionId, collapsed: boolean) => void; children: ReactNode;
}) {
  return collapsible ? <details className="section overview-section" id={`section-${id}`} open={!collapsed} onToggle={event => onCollapse(id, !event.currentTarget.open)}>
    <summary><h2>{title}</h2><span>{meta}</span></summary><div className="section-body">{children}</div>
  </details> : <section className="section" id={`section-${id}`} aria-label={title}>
    <div className="section-title"><h2>{title}</h2><span>{meta}</span></div>{children}
  </section>;
}
