import type { Evaluation, JobDetail, Requirement } from "./client";

export type TabName = "results" | "criteria" | "upload";

export type EligibilityFilter = "all" | Evaluation["eligibility"];
export type OutcomeFilter = "all" | "met" | "partial" | "not_met" | "unknown";
export type StatusFilter =
	| "all"
	| "pending"
	| "processing"
	| "complete"
	| "failed";

export const ELIGIBILITY_FILTERS: EligibilityFilter[] = [
	"all",
	"eligible",
	"needs_review",
	"not_eligible",
];

export const parseEligibilityFilter = (
	value: string | null,
): EligibilityFilter =>
	ELIGIBILITY_FILTERS.find((filter) => filter === value) ?? "all";

export type Invitation = {
	token: string;
	passcode: string;
	expiresAt: string;
};

// Open-ended application windows are stored with a far-future close date.
const OPEN_ENDED_YEAR = 2099;

export const applicationStatus = (job: JobDetail): string => {
	if (!job.applicationOpensAt || !job.applicationClosesAt)
		return "No application window";
	const now = Date.now();
	const opens = new Date(job.applicationOpensAt);
	const closes = new Date(job.applicationClosesAt);
	if (now < opens.getTime()) return `Opens ${opens.toLocaleDateString()}`;
	if (now >= closes.getTime()) return "Applications closed";
	if (closes.getUTCFullYear() >= OPEN_ENDED_YEAR) return "Always open";
	return `Open until ${closes.toLocaleDateString()}`;
};

const ELIGIBILITY_LABELS: Record<Evaluation["eligibility"], string> = {
	pending: "Pending",
	eligible: "Eligible",
	needs_review: "Needs review",
	not_eligible: "Not eligible",
};

export const eligibilityLabel = (eligibility: Evaluation["eligibility"]) =>
	ELIGIBILITY_LABELS[eligibility];

const OUTCOME_LABELS: Record<
	Evaluation["assessments"][number]["outcome"],
	string
> = {
	met: "Met",
	partial: "Partial",
	not_met: "Not met",
	unknown: "Unknown",
};

export const outcomeLabel = (
	outcome: Evaluation["assessments"][number]["outcome"],
) => OUTCOME_LABELS[outcome];

// datetime-local inputs need "YYYY-MM-DDTHH:mm" in local time.
export const toLocalInput = (iso: string | null) =>
	iso
		? new Date(iso).toLocaleString("sv").replace(" ", "T").slice(0, 16)
		: "";

export const draftsToRequirements = (job: JobDetail): Requirement[] =>
	job.draftRequirements.map((requirement) => ({
		...requirement,
		kind:
			requirement.assessability === "resume_evidence"
				? requirement.suggestedKind
				: "ignored",
		weight: requirement.suggestedWeight,
	}));

export const eligibilityChipClass = (
	eligibility: Evaluation["eligibility"],
): string =>
	eligibility === "eligible"
		? "status-chip chip-solid"
		: eligibility === "needs_review"
			? "status-chip chip-outline"
			: eligibility === "not_eligible"
				? "status-chip chip-soft"
				: "status-chip chip-muted";

export const outcomeChipClass = (
	outcome: Evaluation["assessments"][number]["outcome"],
): string =>
	outcome === "met"
		? "status-chip chip-solid"
		: outcome === "partial"
			? "status-chip chip-outline"
			: outcome === "not_met"
				? "status-chip chip-soft"
				: "status-chip chip-muted";

type OverlayProps = {
	onDismiss: () => void;
	labelledBy: string;
};

export const overlayBackdrop = ({ onDismiss, labelledBy }: OverlayProps) => ({
	"aria-labelledby": labelledBy,
	"aria-modal": true as const,
	className: "modal-backdrop",
	onClick: (event: React.MouseEvent<HTMLDivElement>) => {
		if (event.target === event.currentTarget) onDismiss();
	},
	onKeyDown: (event: React.KeyboardEvent<HTMLDivElement>) => {
		if (event.key === "Escape") onDismiss();
	},
	role: "dialog" as const,
});
