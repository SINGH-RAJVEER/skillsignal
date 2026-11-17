import { Button } from "@skillsignal/ui/components/button";
import { Input } from "@skillsignal/ui/components/input";
import { Label } from "@skillsignal/ui/components/label";
import {
	Popover,
	PopoverContent,
	PopoverTrigger,
} from "@skillsignal/ui/components/popover";
import {
	ToggleGroup,
	ToggleGroupItem,
} from "@skillsignal/ui/components/toggle-group";
import {
	Download,
	FileText,
	SlidersHorizontal,
	UploadCloud,
} from "lucide-react";
import type { KeyboardEvent } from "react";
import { ThinkingOrb } from "thinking-orbs";
import type { Evaluation } from "./client";
import { EvidencePanel } from "./evidence-panel";
import {
	ELIGIBILITY_FILTERS,
	type EligibilityFilter,
	eligibilityChipClass,
	eligibilityLabel,
	type OutcomeFilter,
	type StatusFilter,
} from "./shared";

type ResultsTabProps = {
	allEvaluations: Evaluation[];
	visibleEvaluations: Evaluation[];
	selectedEvaluation: Evaluation | null;
	evaluationQuery: string;
	eligibilityFilter: EligibilityFilter;
	outcomeFilter: OutcomeFilter;
	statusFilter: StatusFilter;
	skillFilter: string;
	minimumScoreText: string;
	setEvaluationQuery: (value: string) => void;
	setEligibilityFilter: (value: EligibilityFilter) => void;
	setOutcomeFilter: (value: OutcomeFilter) => void;
	setStatusFilter: (value: StatusFilter) => void;
	setSkillFilter: (value: string) => void;
	setMinimumScoreText: (value: string) => void;
	exportCsv: () => void;
	onAddResumes: () => void;
	onSelect: (evaluation: Evaluation) => void;
};

const isPending = (evaluation: Evaluation) =>
	evaluation.status === "pending" || evaluation.status === "processing";

export const ResultsTab = ({
	allEvaluations,
	visibleEvaluations,
	selectedEvaluation,
	evaluationQuery,
	eligibilityFilter,
	outcomeFilter,
	statusFilter,
	skillFilter,
	minimumScoreText,
	setEvaluationQuery,
	setEligibilityFilter,
	setOutcomeFilter,
	setStatusFilter,
	setSkillFilter,
	setMinimumScoreText,
	exportCsv,
	onAddResumes,
	onSelect,
}: ResultsTabProps) => {
	const countFor = (filter: EligibilityFilter) =>
		filter === "all"
			? allEvaluations.length
			: allEvaluations.filter(
					(evaluation) => evaluation.eligibility === filter,
				).length;
	const processingCount = allEvaluations.filter(isPending).length;
	const topScore = allEvaluations.reduce<number | null>(
		(best, evaluation) =>
			evaluation.score !== null &&
			(best === null || evaluation.score > best)
				? evaluation.score
				: best,
		null,
	);
	const extraFilterCount = [
		outcomeFilter !== "all",
		statusFilter !== "all",
		skillFilter.trim() !== "",
		minimumScoreText.trim() !== "",
	].filter(Boolean).length;
	const selectedIndex = selectedEvaluation
		? visibleEvaluations.findIndex(
				(evaluation) => evaluation.id === selectedEvaluation.id,
			)
		: -1;
	const neighbour = (offset: number) => {
		const target = visibleEvaluations[selectedIndex + offset];
		return target ? () => onSelect(target) : null;
	};
	const onListKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
		const offset =
			event.key === "ArrowDown" ? 1 : event.key === "ArrowUp" ? -1 : 0;
		const target = offset
			? visibleEvaluations[selectedIndex + offset]
			: null;
		if (!target) return;
		event.preventDefault();
		onSelect(target);
		document.getElementById(`candidate-${target.id}`)?.focus();
	};

	if (allEvaluations.length === 0) {
		return (
			<div className="empty-state" data-tour="employer-results">
				<FileText aria-hidden />
				<h3>No resumes evaluated yet</h3>
				<p>
					Add resumes or share an invitation once criteria are
					confirmed.
				</p>
				<Button onClick={onAddResumes} size="sm">
					<UploadCloud />
					Add resumes
				</Button>
			</div>
		);
	}

	return (
		<div className="results-view" data-tour="employer-results">
			<p className="results-summary">
				<span>
					<strong>{allEvaluations.length}</strong> evaluated
				</span>
				<span>
					<strong>{countFor("eligible")}</strong> eligible
				</span>
				<span>
					<strong>{countFor("needs_review")}</strong> need review
				</span>
				<span>
					<strong>{countFor("not_eligible")}</strong> not eligible
				</span>
				{processingCount > 0 && (
					<span>
						<strong>{processingCount}</strong> processing
					</span>
				)}
				<span>
					top score <strong>{topScore ?? "–"}</strong>
				</span>
			</p>

			<div className="results-filters" data-tour="employer-filters">
				<Input
					aria-label="Search candidates"
					className="results-search"
					onChange={(event) => setEvaluationQuery(event.target.value)}
					placeholder="Search name or email"
					value={evaluationQuery}
				/>
				<ToggleGroup
					aria-label="Filter by eligibility"
					className="eligibility-toggle"
					onValueChange={(value) => {
						if (value)
							setEligibilityFilter(value as EligibilityFilter);
					}}
					size="sm"
					type="single"
					value={eligibilityFilter}
					variant="outline"
				>
					{ELIGIBILITY_FILTERS.map((filter) => (
						<ToggleGroupItem key={filter} value={filter}>
							{filter === "all"
								? "All"
								: eligibilityLabel(filter)}
							<span className="toggle-count">
								{countFor(filter)}
							</span>
						</ToggleGroupItem>
					))}
				</ToggleGroup>
				<Popover>
					<PopoverTrigger asChild>
						<Button size="sm" variant="outline">
							<SlidersHorizontal />
							More filters
							{extraFilterCount > 0 && (
								<span className="toggle-count">
									{extraFilterCount}
								</span>
							)}
						</Button>
					</PopoverTrigger>
					<PopoverContent align="start" className="filter-popover">
						<div className="form-field">
							<Label htmlFor="filter-outcome">
								Requirement outcome
							</Label>
							<select
								className="workspace-filter-select"
								id="filter-outcome"
								onChange={(event) =>
									setOutcomeFilter(
										event.target.value as OutcomeFilter,
									)
								}
								value={outcomeFilter}
							>
								<option value="all">Any outcome</option>
								<option value="met">
									Has a met requirement
								</option>
								<option value="partial">
									Has a partial requirement
								</option>
								<option value="not_met">
									Has an unmet requirement
								</option>
								<option value="unknown">
									Has an unknown requirement
								</option>
							</select>
						</div>
						<div className="form-field">
							<Label htmlFor="filter-status">
								Processing state
							</Label>
							<select
								className="workspace-filter-select"
								id="filter-status"
								onChange={(event) =>
									setStatusFilter(
										event.target.value as StatusFilter,
									)
								}
								value={statusFilter}
							>
								<option value="all">Any state</option>
								<option value="pending">Pending</option>
								<option value="processing">Processing</option>
								<option value="complete">Complete</option>
								<option value="failed">Failed</option>
							</select>
						</div>
						<div className="form-field">
							<Label htmlFor="filter-skill">Skill</Label>
							<Input
								id="filter-skill"
								onChange={(event) =>
									setSkillFilter(event.target.value)
								}
								placeholder="Go, Kubernetes..."
								value={skillFilter}
							/>
						</div>
						<div className="form-field">
							<Label htmlFor="filter-minimum">
								Minimum score
							</Label>
							<Input
								id="filter-minimum"
								max={100}
								min={0}
								onChange={(event) =>
									setMinimumScoreText(event.target.value)
								}
								placeholder="0"
								type="number"
								value={minimumScoreText}
							/>
						</div>
						{extraFilterCount > 0 && (
							<Button
								onClick={() => {
									setOutcomeFilter("all");
									setStatusFilter("all");
									setSkillFilter("");
									setMinimumScoreText("");
								}}
								size="sm"
								variant="ghost"
							>
								Clear filters
							</Button>
						)}
					</PopoverContent>
				</Popover>
				<Button
					className="results-export"
					onClick={exportCsv}
					size="sm"
					variant="ghost"
				>
					<Download />
					Export CSV
				</Button>
			</div>

			<div className="review-split">
				<div
					aria-label="Candidates"
					className="candidate-list"
					onKeyDown={onListKeyDown}
					role="listbox"
					tabIndex={-1}
				>
					{visibleEvaluations.map((evaluation) => (
						<button
							aria-selected={
								evaluation.id === selectedEvaluation?.id
							}
							className="candidate-row"
							id={`candidate-${evaluation.id}`}
							key={evaluation.id}
							onClick={() => onSelect(evaluation)}
							role="option"
							type="button"
						>
							<span className="candidate-row-top">
								<strong>
									{evaluation.candidateName ?? "Candidate"}
								</strong>
								<span className="candidate-row-score">
									{evaluation.score ??
										(isPending(evaluation) ? (
											<ThinkingOrb
												aria-hidden
												size={20}
												state="solving"
											/>
										) : (
											"–"
										))}
								</span>
							</span>
							<span className="candidate-row-bottom">
								<span className="candidate-row-meta">
									{candidateSummary(evaluation)}
								</span>
								<span
									className={eligibilityChipClass(
										evaluation.eligibility,
									)}
								>
									{eligibilityLabel(evaluation.eligibility)}
								</span>
							</span>
						</button>
					))}
					{visibleEvaluations.length === 0 && (
						<p className="candidate-list-empty">
							No candidates match these filters.
						</p>
					)}
					<p className="candidate-list-hint">
						Use the arrow keys to move between candidates.
					</p>
				</div>
				{selectedEvaluation ? (
					<EvidencePanel
						evaluation={selectedEvaluation}
						onNext={neighbour(1)}
						onPrevious={neighbour(-1)}
					/>
				) : (
					<p className="evidence-empty">
						Select a candidate to read the evidence behind their
						score.
					</p>
				)}
			</div>
		</div>
	);
};

const OUTCOME_SUMMARY = [
	["met", "met"],
	["partial", "partial"],
	["not_met", "not met"],
	["unknown", "unknown"],
] as const;

const candidateSummary = (evaluation: Evaluation) => {
	if (evaluation.status === "pending" || evaluation.status === "processing")
		return "Processing";
	if (evaluation.status === "failed") return "Processing failed";
	const failedGates = (evaluation.hardGates ?? []).filter(
		(gate) => gate.outcome === "not_met",
	).length;
	if (failedGates > 0)
		return `${failedGates} hard gate${failedGates === 1 ? "" : "s"} failed`;
	const counts = new Map<string, number>();
	for (const assessment of evaluation.assessments ?? []) {
		counts.set(
			assessment.outcome,
			(counts.get(assessment.outcome) ?? 0) + 1,
		);
	}
	const parts = OUTCOME_SUMMARY.filter(([outcome]) =>
		counts.has(outcome),
	).map(([outcome, label]) => `${counts.get(outcome)} ${label}`);
	return parts.length > 0
		? parts.join(" · ")
		: evaluation.coverage !== null
			? `${evaluation.coverage}% evidence coverage`
			: "No assessments";
};
