export type DemoAct = "employer" | "candidate";

export type DemoTourStep = {
	title: string;
	body: string;
	target: string;
	action?: string;
};

export const DEMO_STEPS: Record<DemoAct, DemoTourStep[]> = {
	employer: [
		{
			title: "Employer workspace",
			body: "The employer act starts with a seeded organization and a confirmed role, ready to explore.",
			target: "employer-header",
		},
		{
			title: "A confirmed role",
			body: "This role has a confirmed version of its requirements. Every submission is evaluated against that version.",
			target: "employer-role",
		},
		{
			title: "Evaluation results",
			body: "The seeded submissions show eligible, needs review, and not eligible outcomes with their evidence-backed scores.",
			target: "employer-results",
			action: "employer-results",
		},
		{
			title: "Filter the results",
			body: "Narrow the list by eligibility, requirement outcome, processing state, skill, score, or candidate name.",
			target: "employer-filters",
		},
		{
			title: "Inspect evidence",
			body: "Open a result to see the resume excerpts behind each requirement assessment.",
			target: "employer-evidence-drawer",
			action: "employer-evidence",
		},
		{
			title: "Export the current view",
			body: "Exports use the active filters and let the employer choose which columns to include.",
			target: "employer-export",
			action: "employer-export",
		},
	],
	candidate: [
		{
			title: "Private candidate report",
			body: "The candidate act has its own private, seeded report. It is separate from the employer’s screening results.",
			target: "candidate-report",
			action: "candidate-report",
		},
		{
			title: "Your private archive",
			body: "Past private checks stay in the candidate’s archive and can be reopened here.",
			target: "candidate-history",
			action: "candidate-history",
		},
		{
			title: "Submission receipt",
			body: "The candidate can see their own submission receipt. It contains no employer evaluation or decision.",
			target: "candidate-receipts",
			action: "candidate-receipts",
		},
	],
};
