import { forwardRef } from "react";
import { cn } from "@/lib/utils";
import {
  GlobeAltIcon,
  FingerPrintIcon,
  BanknotesIcon,
  WrenchScrewdriverIcon,
} from "@heroicons/react/24/outline";

const steps = [
  {
    icon: GlobeAltIcon,
    title: "Discover",
    description: "Via SKILL.md, MCP, DID, or OAuth metadata.",
  },
  {
    icon: FingerPrintIcon,
    title: "Authenticate",
    description: "Wallet signature or x402 — account created instantly.",
  },
  {
    icon: BanknotesIcon,
    title: "Pay",
    description: "x402 USDC, crypto deposit, or card.",
  },
  {
    icon: WrenchScrewdriverIcon,
    title: "Operate",
    description: "Streams, diagnostics, billing — all via MCP.",
  },
];

const AgentPipelineStrip = forwardRef(
  ({ className, headingLevel: Heading = "h4", ...props }, ref) => (
    <div ref={ref} className={cn("agent-pipeline", className)} {...props}>
      {steps.map((step, i) => (
        <div key={step.title} className="agent-pipeline__step">
          <div className="agent-pipeline__icon-wrap">
            <step.icon className="agent-pipeline__icon" />
          </div>
          <div className="agent-pipeline__content">
            <Heading className="agent-pipeline__title">{step.title}</Heading>
            <p className="agent-pipeline__desc">{step.description}</p>
          </div>
          {i < steps.length - 1 && (
            <div className="agent-pipeline__connector" aria-hidden="true">
              <svg viewBox="0 0 24 12" fill="none" stroke="currentColor" strokeWidth="1.5">
                <path d="M0 6H20M16 1L22 6L16 11" />
              </svg>
            </div>
          )}
        </div>
      ))}
    </div>
  )
);

AgentPipelineStrip.displayName = "AgentPipelineStrip";

export default AgentPipelineStrip;
