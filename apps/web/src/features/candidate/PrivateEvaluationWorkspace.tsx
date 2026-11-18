import { Button } from "@skillsignal/ui/components/button";
import { Label } from "@skillsignal/ui/components/label";
import { Textarea } from "@skillsignal/ui/components/textarea";
import {
	ToggleGroup,
	ToggleGroupItem,
} from "@skillsignal/ui/components/toggle-group";
import { AlertTriangle, FileDown, FileSearch, Trash2 } from "lucide-react";
import { type FormEvent, useCallback, useEffect, useState } from "react";
import { ThinkingOrb } from "thinking-orbs";
import { useDemoTourAction } from "../tour/DemoTour";
import { BillingStrip, PackPurchase, usePointsSummary } from "./BillingPanel";
import {
	candidateClient,
	type EmploymentFact,
	type IndependentEvaluation,
	type PointPack,
} from "./client";
import { FileDrop } from "./FileDrop";

const PROCESSING_STEPS = [
	"Reading the document",
	"Checking documented facts",
	"Preparing the private report",
];

const isPendingStatus = (status: IndependentEvaluation["status"]) =>
	status === "queued" || status === "processing";

const reportScore = (evaluation: IndependentEvaluation) =>
	evaluation.jobDescriptionProvided
		? evaluation.score
		: (evaluation.generalQualityScore ?? evaluation.score);

const reportScoreLabel = (evaluation: IndependentEvaluation) => {
	const score = reportScore(evaluation);
	return score === null ? evaluation.status : `${score}/100`;
};

export type CandidateView = "check" | "history" | "submissions";

type PrivateEvaluationWorkspaceProps = {
	view: "check" | "history";
	onViewChange: (view: CandidateView) => void;
};

export const PrivateEvaluationWorkspace = ({
	view,
	onViewChange,
}: PrivateEvaluationWorkspaceProps) => {
	const [isWorking, setIsWorking] = useState(false);
	const [isLoadingHistory, setIsLoadingHistory] = useState(true);
	const [error, setError] = useState<string | null>(null);
	const [historyError, setHistoryError] = useState<string | null>(null);
	const [privateResume, setPrivateResume] = useState<File | null>(null);
	const [jobDescription, setJobDescription] = useState("");
	const [jobDescriptionFile, setJobDescriptionFile] = useState<File | null>(
		null,
	);
	const [jobSource, setJobSource] = useState<"paste" | "upload">("paste");
	const [privateEvaluation, setPrivateEvaluation] =
		useState<IndependentEvaluation | null>(null);
	const [privateHistory, setPrivateHistory] = useState<
		IndependentEvaluation[]
	>([]);
	const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null);
	const [deletingId, setDeletingId] = useState<string | null>(null);
	const [packs, setPacks] = useState<PointPack[]>([]);
	const [showPurchase, setShowPurchase] = useState(false);
	const { points, quote, refreshPoints } = usePointsSummary();
	const historyIsUnavailable =
		historyError !== null && privateHistory.length === 0;

	useEffect(() => {
		candidateClient
			.packs()
			.then(setPacks)
			.catch(() => setPacks([]));
	}, []);

	const loadPrivateHistory = useCallback(async () => {
		setHistoryError(null);
		try {
			setPrivateHistory(await candidateClient.independentEvaluations());
		} catch {
			setHistoryError("Previous checks could not be loaded.");
		} finally {
			setIsLoadingHistory(false);
		}
	}, []);

	useEffect(() => {
		void loadPrivateHistory();
	}, [loadPrivateHistory]);

	useEffect(() => {
		if (!privateEvaluation || !isPendingStatus(privateEvaluation.status))
			return;
		const interval = window.setInterval(async () => {
			try {
				const result = await candidateClient.independentEvaluation(
					privateEvaluation.id,
				);
				setPrivateEvaluation(result);
				if (!isPendingStatus(result.status)) void loadPrivateHistory();
			} catch {
				setPrivateEvaluation(null);
				setError("Your report could not be loaded. Try again.");
			}
		}, 2_000);
		return () => window.clearInterval(interval);
	}, [loadPrivateHistory, privateEvaluation]);

	const startPrivateEvaluation = async (
		event: FormEvent<HTMLFormElement>,
	) => {
		event.preventDefault();
		if (!privateResume) return;
		setError(null);
		setIsWorking(true);
		try {
			const result = await candidateClient.createIndependentEvaluation(
				privateResume,
				jobDescription,
				jobDescriptionFile,
			);
			setPrivateEvaluation({
				id: result.id,
				originalName: privateResume.name,
				status: "queued",
				score: null,
				safeError: null,
				jobDescriptionProvided: Boolean(
					jobDescription.trim() || jobDescriptionFile,
				),
				createdAt: new Date().toISOString(),
				completedAt: null,
			});
			setPrivateResume(null);
			setJobDescription("");
			setJobDescriptionFile(null);
			setShowPurchase(false);
			refreshPoints();
		} catch (reason) {
			const message =
				reason instanceof Error
					? reason.message
					: "Resume check could not start";
			setError(message);
			if (message.includes("points")) {
				setShowPurchase(true);
				candidateClient
					.packs()
					.then(setPacks)
					.catch(() => undefined);
			}
		} finally {
			setIsWorking(false);
		}
	};

	const openPrivateEvaluation = useCallback(
		async (evaluationId: string) => {
			setError(null);
			try {
				setPrivateEvaluation(
					await candidateClient.independentEvaluation(evaluationId),
				);
				onViewChange("check");
			} catch (reason) {
				setError(
					reason instanceof Error
						? reason.message
						: "Report could not be opened",
				);
			}
		},
		[onViewChange],
	);
	const showSeededReport = useCallback(async () => {
		try {
			const history = await candidateClient.independentEvaluations();
			setPrivateHistory(history);
			const evaluation =
				history.find((item) => item.status === "complete") ??
				history[0];
			if (evaluation) setPrivateEvaluation(evaluation);
			onViewChange("check");
		} catch (reason) {
			setError(
				reason instanceof Error
					? reason.message
					: "The seeded private report could not be loaded.",
			);
		}
	}, [onViewChange]);
	const showHistory = useCallback(
		() => onViewChange("history"),
		[onViewChange],
	);
	useDemoTourAction("candidate-report", showSeededReport);
	useDemoTourAction("candidate-history", showHistory);

	const deleteEvaluation = async (evaluationId: string) => {
		setDeletingId(evaluationId);
		setHistoryError(null);
		try {
			await candidateClient.deleteIndependentEvaluation(evaluationId);
			setPrivateHistory((history) =>
				history.filter((evaluation) => evaluation.id !== evaluationId),
			);
			if (privateEvaluation?.id === evaluationId)
				setPrivateEvaluation(null);
			setConfirmDeleteId(null);
		} catch (reason) {
			setHistoryError(
				reason instanceof Error
					? reason.message
					: "The report could not be deleted.",
			);
		} finally {
			setDeletingId(null);
		}
	};

	if (view === "history") {
		return (
			<section
				aria-labelledby="history-title"
				className="candidate-section"
				data-tour="candidate-history"
			>
				<header className="candidate-section-head">
					<h1 id="history-title">Previous checks</h1>
					<p>
						Reports stay private to you. Delete any report and its
						files.
					</p>
				</header>
				{historyError && (
					<div className="candidate-empty" role="alert">
						<p>{historyError}</p>
						{privateHistory.length === 0 && (
							<Button
								onClick={() => void loadPrivateHistory()}
								size="sm"
								variant="outline"
							>
								Try again
							</Button>
						)}
					</div>
				)}
				{isLoadingHistory ? (
					<ListSkeleton label="Loading your checks" />
				) : historyIsUnavailable ? null : privateHistory.length ===
					0 ? (
					<p className="candidate-empty">
						Completed and in-progress checks will appear here.
					</p>
				) : (
					<ul className="candidate-history-list">
						{privateHistory.map((evaluation) => (
							<li key={evaluation.id}>
								{confirmDeleteId === evaluation.id ? (
									<div className="history-confirm-delete">
										<p>Delete this report and its files?</p>
										<div className="history-confirm-actions">
											<Button
												disabled={
													deletingId === evaluation.id
												}
												onClick={() =>
													void deleteEvaluation(
														evaluation.id,
													)
												}
												size="xs"
												variant="destructive"
											>
												{deletingId === evaluation.id
													? "Deleting"
													: "Delete"}
											</Button>
											<Button
												onClick={() =>
													setConfirmDeleteId(null)
												}
												size="xs"
												variant="ghost"
											>
												Keep it
											</Button>
										</div>
									</div>
								) : (
									<>
										<button
											className="history-open"
											onClick={() =>
												void openPrivateEvaluation(
													evaluation.id,
												)
											}
											type="button"
										>
											<span className="history-name">
												{evaluation.originalName}
											</span>
											<span className="history-meta">
												{evaluation.jobDescriptionProvided
													? "With job description"
													: "General resume check"}
												{" · "}
												{new Date(
													evaluation.createdAt,
												).toLocaleDateString()}
											</span>
										</button>
										<strong className="history-score">
											{reportScoreLabel(evaluation)}
										</strong>
										<Button
											aria-label={`Delete ${evaluation.originalName}`}
											onClick={() =>
												setConfirmDeleteId(
													evaluation.id,
												)
											}
											size="icon-xs"
											variant="ghost"
										>
											<Trash2 aria-hidden />
										</Button>
									</>
								)}
							</li>
						))}
					</ul>
				)}
			</section>
		);
	}

	if (privateEvaluation) {
		return (
			<PrivateReport
				evaluation={privateEvaluation}
				onClose={() => setPrivateEvaluation(null)}
			/>
		);
	}

	const recentChecks = privateHistory.slice(0, 3);

	return (
		<section
			aria-labelledby="private-check-title"
			className="candidate-section"
		>
			<header className="candidate-section-head">
				<h1 id="private-check-title">Check your resume</h1>
				<p>
					Private to you. Add a job description for
					criterion-by-criterion feedback.
				</p>
			</header>

			<form className="check-steps" onSubmit={startPrivateEvaluation}>
				<div className="check-step">
					<span aria-hidden className="check-step-index">
						1
					</span>
					<div className="form-field">
						<Label htmlFor="private-resume">Resume</Label>
						<FileDrop
							accept=".pdf,.docx,.txt"
							file={privateResume}
							hint="PDF, DOCX, or TXT up to 20 MB"
							id="private-resume"
							onFile={setPrivateResume}
							prompt="Drop your resume here"
						/>
					</div>
				</div>

				<div className="check-step">
					<span aria-hidden className="check-step-index">
						2
					</span>
					<div className="form-field">
						<div className="check-step-label">
							<Label
								htmlFor={
									jobSource === "paste"
										? "job-description"
										: "job-description-file"
								}
							>
								Job description <span>optional</span>
							</Label>
							<ToggleGroup
								aria-label="Job description source"
								onValueChange={(value) => {
									if (value !== "paste" && value !== "upload")
										return;
									setJobSource(value);
									setJobDescription("");
									setJobDescriptionFile(null);
								}}
								size="sm"
								type="single"
								value={jobSource}
								variant="outline"
							>
								<ToggleGroupItem value="paste">
									Paste
								</ToggleGroupItem>
								<ToggleGroupItem value="upload">
									Upload file
								</ToggleGroupItem>
							</ToggleGroup>
						</div>
						{jobSource === "paste" ? (
							<Textarea
								id="job-description"
								onChange={(event) =>
									setJobDescription(event.currentTarget.value)
								}
								placeholder="Paste the role description"
								rows={6}
								value={jobDescription}
							/>
						) : (
							<FileDrop
								accept=".pdf,.docx,.txt,.md,.markdown,.html,.htm"
								file={jobDescriptionFile}
								hint="PDF, DOCX, TXT, Markdown, or HTML up to 10 MB"
								id="job-description-file"
								onFile={setJobDescriptionFile}
								prompt="Drop the job description here"
							/>
						)}
					</div>
				</div>

				<div className="check-step">
					<span
						aria-hidden
						className="check-step-index check-step-final"
					>
						3
					</span>
					<div className="form-field">
						<Button
							className="w-full"
							disabled={!privateResume || isWorking}
							size="lg"
							type="submit"
						>
							{isWorking ? (
								<ThinkingOrb
									aria-hidden
									size={20}
									state="solving"
								/>
							) : (
								<FileSearch aria-hidden />
							)}
							{isWorking ? "Starting check" : "Run private check"}
						</Button>
						<BillingStrip points={points} quote={quote} />
					</div>
				</div>
			</form>

			{error && (
				<p className="form-error" role="alert">
					{error}
				</p>
			)}
			{showPurchase && (
				<PackPurchase
					onPurchased={() => {
						setShowPurchase(false);
						setError(null);
						refreshPoints();
					}}
					packs={packs}
				/>
			)}
			{!showPurchase && points?.allowance?.freeUsedThisWeek && (
				<Button
					className="justify-self-start"
					onClick={() => setShowPurchase(true)}
					size="sm"
					variant="outline"
				>
					Buy points
				</Button>
			)}

			<section aria-labelledby="recent-title" className="recent-checks">
				<div className="recent-checks-head">
					<h2 id="recent-title">Recent checks</h2>
					{privateHistory.length > recentChecks.length && (
						<Button
							onClick={() => onViewChange("history")}
							size="sm"
							variant="ghost"
						>
							View all
						</Button>
					)}
				</div>
				{isLoadingHistory ? (
					<ListSkeleton label="Loading your checks" />
				) : recentChecks.length === 0 ? (
					<p className="candidate-empty">
						Your private reports will appear here.
					</p>
				) : (
					<ul className="recent-list">
						{recentChecks.map((evaluation) => (
							<li key={evaluation.id}>
								<button
									onClick={() =>
										void openPrivateEvaluation(
											evaluation.id,
										)
									}
									type="button"
								>
									<span>{evaluation.originalName}</span>
									<span className="history-meta">
										{new Date(
											evaluation.createdAt,
										).toLocaleDateString()}
										{" · "}
										<strong>
											{reportScoreLabel(evaluation)}
										</strong>
									</span>
								</button>
							</li>
						))}
					</ul>
				)}
			</section>

			<p className="passcode-prompt">
				Have an application passcode from an employer?{" "}
				<button
					onClick={() => onViewChange("submissions")}
					type="button"
				>
					Submit your resume
				</button>
			</p>
		</section>
	);
};

const ListSkeleton = ({ label }: { label: string }) => (
	<div aria-label={label} className="list-skeleton" role="status">
		<span />
		<span />
		<span />
	</div>
);

const ProcessingState = () => {
	const [stepIndex, setStepIndex] = useState(0);

	useEffect(() => {
		if (stepIndex >= PROCESSING_STEPS.length - 1) return;
		const timeout = window.setTimeout(() => {
			setStepIndex((index) => index + 1);
		}, 1_800);
		return () => window.clearTimeout(timeout);
	}, [stepIndex]);

	return (
		<div className="report-progress" role="status" aria-live="polite">
			<div className="report-progress-heading">
				<ThinkingOrb aria-hidden size={20} state="solving" />
				<div>
					<strong>Your private report is in progress</strong>
					<p>{PROCESSING_STEPS[stepIndex]}</p>
				</div>
			</div>
			<ol>
				{PROCESSING_STEPS.map((step, index) => (
					<li data-active={index === stepIndex} key={step}>
						{step}
					</li>
				))}
			</ol>
			<small>
				You can leave this page and reopen the report from your private
				archive.
			</small>
		</div>
	);
};

const groupByCategory = (
	skills: Array<{ canonicalName: string; category?: string | null }>,
): Array<[string, string[]]> => {
	const grouped = new Map<string, string[]>();
	for (const skill of skills) {
		const category = skill.category ?? "Other";
		grouped.set(category, [
			...(grouped.get(category) ?? []),
			skill.canonicalName,
		]);
	}
	return [...grouped.entries()].sort(([a], [b]) => a.localeCompare(b));
};

const employmentLine = (role: EmploymentFact) => {
	const title = [role.title, role.employer].filter(Boolean).join(" at ");
	const end = role.isCurrent ? "present" : role.endDate;
	const dates = [role.startDate, end].filter(Boolean).join(" to ");
	return dates ? `${title}, ${dates}` : title;
};

const readinessLabel = (score: number | null) => {
	if (score === null) return "Not enough documented information";
	if (score >= 80) return "Strong document coverage";
	if (score >= 60) return "Useful foundation, with gaps";
	return "Important sections need attention";
};

const PrivateReport = ({
	evaluation,
	onClose,
}: {
	evaluation: IndependentEvaluation;
	onClose: () => void;
}) => {
	const [downloadError, setDownloadError] = useState<string | null>(null);

	if (evaluation.status === "failed") {
		return (
			<div className="private-report report-failure">
				<AlertTriangle aria-hidden />
				<h3>This document could not be checked</h3>
				<p>
					{evaluation.safeError ??
						"Upload a digital PDF, DOCX, or TXT file."}
				</p>
				<Button onClick={onClose} size="sm" variant="outline">
					Try another document
				</Button>
			</div>
		);
	}
	if (isPendingStatus(evaluation.status)) return <ProcessingState />;

	const facts = evaluation.facts ?? {};
	const skillGroups = groupByCategory(facts.skills ?? []);
	const warnings = facts.warnings ?? [];

	return (
		<article
			className="private-report report-preview"
			data-tour="candidate-report"
		>
			<header>
				<div>
					<span>{evaluation.originalName}</span>
					{evaluation.jobDescriptionProvided && (
						<b className="report-context">Role guidance included</b>
					)}
				</div>
				<span>
					{evaluation.completedAt
						? new Date(evaluation.completedAt).toLocaleDateString()
						: null}
				</span>
			</header>
			<section
				className="report-score"
				aria-label={
					evaluation.jobDescriptionProvided
						? "Documented role fit"
						: "Resume readiness"
				}
			>
				<strong>{reportScore(evaluation) ?? "–"}</strong>
				<span>
					of 100{" "}
					{evaluation.jobDescriptionProvided
						? "documented role fit"
						: "resume readiness"}
				</span>
				{evaluation.jobDescriptionProvided ? (
					<p>
						Evidence coverage: {evaluation.evidenceCoverage ?? 0}%.
						General resume quality:{" "}
						{evaluation.generalQualityScore ?? "–"}/100.
					</p>
				) : (
					<b>{readinessLabel(reportScore(evaluation))}</b>
				)}
				<p>
					The score describes this document, not your ability. Missing
					evidence is not treated as a proven lack of experience.
				</p>
			</section>
			{(evaluation.requirementAssessments?.length ?? 0) > 0 && (
				<section className="report-section">
					<h4>Job requirement evidence</h4>
					{evaluation.requirementAssessments?.map((assessment) => (
						<div
							className="suggestion-row"
							key={assessment.requirementId}
						>
							<b>{assessment.requirement}</b>
							<p>
								{assessment.outcome.replace(/_/g, " ")}.{" "}
								{assessment.reasoning}
							</p>
							{assessment.evidence.map((item) => (
								<blockquote
									key={`${item.blockId}-${item.quote}`}
								>
									{item.quote}
									<cite>Source block: {item.blockId}</cite>
								</blockquote>
							))}
						</div>
					))}
				</section>
			)}

			{warnings.length > 0 && (
				<section className="report-section report-warnings">
					<h4>Document warnings</h4>
					<ul>
						{warnings.map((warning) => (
							<li key={warning}>{warning}</li>
						))}
					</ul>
				</section>
			)}

			{skillGroups.length > 0 && (
				<section className="report-section">
					<h4>Documented skills</h4>
					{skillGroups.map(([category, names]) => (
						<p key={category}>
							<strong>{category}</strong> {names.join(", ")}
						</p>
					))}
				</section>
			)}

			{(facts.employment?.length ?? 0) > 0 && (
				<section className="report-section">
					<h4>Documented experience</h4>
					{facts.employment?.map((role) => (
						<p
							key={`${role.title}-${role.employer}-${role.startDate}`}
						>
							{employmentLine(role)}
						</p>
					))}
				</section>
			)}

			{((facts.education?.length ?? 0) > 0 ||
				(facts.certifications?.length ?? 0) > 0) && (
				<section className="report-section">
					<h4>Education and credentials</h4>
					{facts.education?.map((entry) => (
						<p
							key={`${entry.degree}-${entry.institution}-${entry.graduationDate}`}
						>
							{[
								entry.degree,
								entry.fieldOfStudy,
								entry.institution,
							]
								.filter(Boolean)
								.join(", ")}
						</p>
					))}
					{facts.certifications?.map((entry) => (
						<p key={`${entry.name}-${entry.issuer}`}>
							{[entry.name, entry.issuer]
								.filter(Boolean)
								.join(", ")}
						</p>
					))}
				</section>
			)}

			<section className="report-section report-suggestions">
				<h4>Changes to consider</h4>
				{(evaluation.suggestions?.length ?? 0) > 0 ? (
					evaluation.suggestions?.map((suggestion) => (
						<div
							className="suggestion-row"
							key={`${suggestion.title}-${suggestion.detail}`}
						>
							<b>{suggestion.title}</b>
							<p>{suggestion.detail}</p>
						</div>
					))
				) : (
					<p>No immediate document changes were identified.</p>
				)}
			</section>

			<footer className="report-actions">
				{evaluation.hasImprovedResume && (
					<Button
						onClick={() => {
							setDownloadError(null);
							candidateClient
								.downloadImprovedResume(evaluation.id)
								.catch(() =>
									setDownloadError(
										"The corrected resume could not be downloaded. Try again.",
									),
								);
						}}
						size="sm"
					>
						<FileDown aria-hidden />
						Download corrected resume
					</Button>
				)}
				<Button onClick={onClose} size="sm" variant="outline">
					Check another resume
				</Button>
				{downloadError && (
					<p className="form-error" role="alert">
						{downloadError}
					</p>
				)}
			</footer>
		</article>
	);
};
