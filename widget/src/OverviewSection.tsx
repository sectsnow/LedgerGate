import type { ReactNode } from "react";
import type { SectionId } from "./reading";

export default function OverviewSection({ id, title, meta, collapsible, collapsed, onCollapse, children, nested = false }: {
  id: SectionId; title: string; meta?: string; collapsible: boolean; collapsed: boolean;
  onCollapse: (id: SectionId, collapsed: boolean) => void; children: ReactNode;
  nested?: boolean;
}) {
  const Heading = nested ? "h3" : "h2";
  return collapsible ? <details className={nested ? "insight-section" : "section overview-section"} id={`section-${id}`} open={!collapsed} onToggle={event => {event.stopPropagation();onCollapse(id, !event.currentTarget.open);}}>
    <summary><Heading>{title}</Heading>{meta && <span>{meta}</span>}</summary><div className="section-body">{children}</div>
  </details> : <section className="section" id={`section-${id}`} aria-label={title}>
    <div className="section-title"><h2>{title}</h2><span>{meta}</span></div>{children}
  </section>;
}
