import { Button } from "@skillsignal/ui/components/button";
import { ArrowLeft, ArrowRight } from "lucide-react";
import type { Evaluation } from "./client";
import {
	eligibilityChipClass,
	eligibilityLabel,
	outcomeChipClass,
	outcomeLabel,
} from "./shared";

type Assessment = Evaluation["assessments"][number];

type EvidencePanelProps = {
	evaluation: Evaluation;
	onPrevious: (() => void) | null;
	onNext: (() => void) | null;
};

export const EvidencePanel = ({
	evaluation,
	onPrevious,
	onNext,
}: EvidencePanelProps) => {
	const assessments = evaluation.assessments ?? [];
	const gateAssessments = assessments.filter(
		(assessment) => assessment.kind === "hard_gate",
	);
	const scoredAssessments = assessments.filter(
		(assessment) => assessment.kind !== "hard_gate",
	);
	const gateSummary = evaluation.hardGates ?? [];
	const warnings = evaluation.qualityWarnings ?? [];
	const metadata = evaluation.extractionMetadata;

	return (
		<article
			aria-labelledby="evidence-title"
			className="evidence-panel"
			data-tour="employer-evidence"
		>
			<header className="evidence-head">
				<div>
					<h2 id="evidence-title">
						{evaluation.candidateName ?? "Candidate"}
					</h2>
					<p className="evidence-contact">
						{[
							evaluation.candidateEmail,
							evaluation.candidateLocation,
						]
							.filter(Boolean)
							.join(" · ")}
					</p>
				</div>
				<div className="evidence-score">
					<strong>
						{evaluation.score ?? "–"}
						<span>/100</span>
					</strong>
					<span
						className={eligibilityChipClass(evaluation.eligibility)}
					>
						{eligibilityLabel(evaluation.eligibility)}
					</span>
				</div>
			</header>

			{(warnings.length > 0 || metadata?.pageCount !== undefined) && (
				<p className="evidence-quality">
					{metadata?.pageCount !== undefined && (
						<span>
							{metadata.pageCount} page
							{metadata.pageCount === 1 ? "" : "s"},{" "}
							{metadata.blockCount ?? 0} evidence block
							{metadata.blockCount === 1 ? "" : "s"}
						</span>
					)}
					{warnings.map((warning) => (
						<span key={warning}>{warning}</span>
					))}
				</p>
			)}

			{(gateAssessments.length > 0 || gateSummary.length > 0) && (
				<section
					aria-labelledby="gates-title"
					className="evidence-gates"
				>
					<div className="evidence-gates-head">
						<h3 id="gates-title">Hard gates</h3>
						<p>
							Gates decide eligibility and are excluded from the
							score.
						</p>
					</div>
					{gateAssessments.length > 0
						? gateAssessments.map((assessment) => (
								<AssessmentRow
									assessment={assessment}
									key={`gate-${assessment.requirement}`}
								/>
							))
						: gateSummary.map((gate) => (
								<p
									className="evidence-gate-line"
									key={gate.requirement}
								>
									<span>{gate.requirement}</span>
									<span className="muted-copy">
										{gate.outcome.replace(/_/g, " ")}
									</span>
								</p>
							))}
				</section>
			)}

			<section
				aria-labelledby="scored-title"
				className="evidence-requirements"
			>
				<div className="evidence-columns" aria-hidden>
					<span id="scored-title">Scored requirements</span>
					<span>Weight</span>
					<span>Outcome</span>
					<span>Share of score</span>
				</div>
				{scoredAssessments.length === 0 ? (
					<p className="muted-copy">
						{assessments.length === 0
							? "Requirement assessments appear once processing completes."
							: "Every confirmed requirement for this role is a hard gate."}
					</p>
				) : (
					scoredAssessments.map((assessment) => (
						<AssessmentRow
							assessment={assessment}
							key={`scored-${assessment.requirement}`}
							scored
						/>
					))
				)}
			</section>

			<footer className="evidence-nav">
				<Button
					disabled={!onPrevious}
					onClick={onPrevious ?? undefined}
					size="sm"
					variant="outline"
				>
					<ArrowLeft />
					Previous
				</Button>
				<Button
					disabled={!onNext}
					onClick={onNext ?? undefined}
					size="sm"
					variant="outline"
				>
					Next candidate
					<ArrowRight />
				</Button>
			</footer>
		</article>
	);
};

const AssessmentRow = ({
	assessment,
	scored = false,
}: {
	assessment: Assessment;
	scored?: boolean;
}) => {
	const contribution = Math.min(
		100,
		Math.max(0, assessment.contribution ?? 0),
	);
	return (
		<div className="assessment-row">
			<div className="assessment-line">
				<strong>{assessment.requirement}</strong>
				<span className="assessment-weight">
					{scored ? (assessment.weight ?? 1) : null}
				</span>
				<span className={outcomeChipClass(assessment.outcome)}>
					{outcomeLabel(assessment.outcome)}
				</span>
				{scored ? (
					<span className="assessment-share">
						<span aria-hidden className="coverage-meter">
							<span
								className="coverage-meter-fill"
								style={{ width: `${contribution}%` }}
							/>
						</span>
						{contribution}%
					</span>
				) : (
					<span />
				)}
			</div>
			<p className="assessment-reasoning">{assessment.reasoning}</p>
			{assessment.evidence.map((item) => (
				<blockquote
					className="evidence-quote"
					key={`${item.blockId}-${item.quote}`}
				>
					{item.quote}
					<cite>Source block {item.blockId}</cite>
				</blockquote>
			))}
			{assessment.semanticEvidence?.matches?.length ? (
				<div className="related-passages">
					<p className="eyebrow">Related passages</p>
					{assessment.semanticEvidence.matches.map((match) => (
						<blockquote
							className="evidence-quote evidence-related"
							key={`semantic-${match.blockId}`}
						>
							{match.text || match.blockId}
							<cite>
								Similarity {Math.round(match.similarity * 100)}%
							</cite>
						</blockquote>
					))}
				</div>
			) : null}
		</div>
	);
};
