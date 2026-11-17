import { Button } from "@skillsignal/ui/components/button";
import { Input } from "@skillsignal/ui/components/input";
import { ArrowLeft, Briefcase, Plus, UploadCloud, X } from "lucide-react";
import {
	type FormEvent,
	useCallback,
	useEffect,
	useMemo,
	useState,
} from "react";
import { useSearchParams } from "react-router-dom";
import { ThinkingOrb } from "thinking-orbs";
import { authClient } from "../../lib/auth-client";
import { useDemoTourAction } from "../tour/DemoTour";
import {
	type Evaluation,
	type EvaluationFilters,
	type Job,
	type JobDetail,
	type Organization,
	type Requirement,
	workspaceClient,
} from "./client";
import { CriteriaTab } from "./criteria-tab";
import { ExportDialog } from "./export-dialog";
import {
	ApplicationWindowDialog,
	CreateJobDialog,
	CreateOrganizationDialog,
} from "./job-dialogs";
import { MembersDialog } from "./members-dialog";
import { ResultsTab } from "./results-tab";
import { ShareMenu } from "./share-menu";
import {
	applicationStatus,
	draftsToRequirements,
	type EligibilityFilter,
	type Invitation,
	type OutcomeFilter,
	parseEligibilityFilter,
	type StatusFilter,
	type TabName,
} from "./shared";
import { UploadTab } from "./upload-tab";

export const Workspace = () => {
	const [searchParams, setSearchParams] = useSearchParams();
	const { data: session, isPending } = authClient.useSession();
	const [organization, setOrganization] = useState<Organization | null>(null);
	const [organizationsLoaded, setOrganizationsLoaded] = useState(false);
	const [jobsLoaded, setJobsLoaded] = useState(false);
	const organizationId = organization?.id ?? "";
	const [jobs, setJobs] = useState<Job[]>([]);
	const [jobSearch, setJobSearch] = useState("");
	const [selectedJob, setSelectedJob] = useState<JobDetail | null>(null);
	const [activeTab, setActiveTab] = useState<TabName>("results");
	const [evaluations, setEvaluations] = useState<Evaluation[]>([]);
	const [selectedEvaluationId, setSelectedEvaluationId] = useState<
		string | null
	>(null);
	const [isCreateOrgOpen, setIsCreateOrgOpen] = useState(false);
	const [isCreateJobOpen, setIsCreateJobOpen] = useState(false);
	const [requirements, setRequirements] = useState<Requirement[]>([]);
	const [resumes, setResumes] = useState<File[]>([]);
	const [uploadInputKey, setUploadInputKey] = useState(0);
	const appendResumes = (files: File[]) =>
		setResumes((current) => [...current, ...files]);
	const [invitation, setInvitation] = useState<Invitation | null>(null);
	const [inviteHours, setInviteHours] = useState(168);
	const [copiedInvitation, setCopiedInvitation] = useState(false);
	const [isCreatingInvitation, setIsCreatingInvitation] = useState(false);
	const [isWindowOpen, setIsWindowOpen] = useState(false);
	const [isMembersOpen, setIsMembersOpen] = useState(false);
	const [evaluationQuery, setEvaluationQuery] = useState(
		searchParams.get("search") ?? "",
	);
	const [eligibilityFilter, setEligibilityFilter] =
		useState<EligibilityFilter>(
			parseEligibilityFilter(searchParams.get("eligibility")),
		);
	const [outcomeFilter, setOutcomeFilter] = useState<OutcomeFilter>(
		(searchParams.get("outcome") as OutcomeFilter) ?? "all",
	);
	const [statusFilter, setStatusFilter] = useState<StatusFilter>(
		(searchParams.get("status") as StatusFilter) ?? "all",
	);
	const [skillFilter, setSkillFilter] = useState(
		searchParams.get("skill") ?? "",
	);
	const [minimumScoreText, setMinimumScoreText] = useState(
		searchParams.get("minimum_score") ?? "",
	);
	const [allEvaluations, setAllEvaluations] = useState<Evaluation[]>([]);
	const [evaluationsJobId, setEvaluationsJobId] = useState<string | null>(
		null,
	);
	const [isExportOpen, setIsExportOpen] = useState(false);
	const [notice, setNotice] = useState<string | null>(null);
	const [error, setError] = useState<string | null>(null);
	const [isConfirming, setIsConfirming] = useState(false);
	const [isUploading, setIsUploading] = useState(false);
	const evaluationFilters = useMemo(() => {
		const filters: EvaluationFilters = {};
		if (eligibilityFilter !== "all")
			filters.eligibility = [eligibilityFilter];
		if (outcomeFilter !== "all") filters.outcome = [outcomeFilter];
		if (statusFilter !== "all") filters.status = [statusFilter];
		if (skillFilter.trim()) filters.skill = skillFilter.trim();
		if (evaluationQuery.trim()) filters.search = evaluationQuery.trim();
		if (
			minimumScoreText.trim() !== "" &&
			Number.isFinite(Number(minimumScoreText))
		) {
			filters.minimumScore = Math.min(
				100,
				Math.max(0, Math.round(Number(minimumScoreText))),
			);
		}
		return filters;
	}, [
		eligibilityFilter,
		outcomeFilter,
		statusFilter,
		skillFilter,
		evaluationQuery,
		minimumScoreText,
	]);

	useEffect(() => {
		const next = new URLSearchParams(searchParams);
		for (const [key, value] of Object.entries({
			search: evaluationQuery,
			eligibility: eligibilityFilter,
			outcome: outcomeFilter,
			status: statusFilter,
			skill: skillFilter,
			minimum_score: minimumScoreText,
		})) {
			if (value && value !== "all") next.set(key, value);
			else next.delete(key);
		}
		if (next.toString() !== searchParams.toString())
			setSearchParams(next, { replace: true });
	}, [
		evaluationQuery,
		eligibilityFilter,
		outcomeFilter,
		statusFilter,
		skillFilter,
		minimumScoreText,
		searchParams,
		setSearchParams,
	]);

	const reportError = useCallback((reason: unknown) => {
		setNotice(null);
		setError(reason instanceof Error ? reason.message : "Request failed");
	}, []);

	const closeOverlays = useCallback(() => {
		setIsCreateOrgOpen(false);
		setIsCreateJobOpen(false);
		setIsWindowOpen(false);
		setIsMembersOpen(false);
		setIsExportOpen(false);
	}, []);

	const refreshEvaluations = useCallback(
		async (jobId: string) => {
			const [results, everything] = await Promise.all([
				workspaceClient.evaluations(jobId, evaluationFilters),
				workspaceClient.evaluations(jobId),
			]);
			setEvaluations(results);
			setAllEvaluations(everything);
			setEvaluationsJobId(jobId);
		},
		[evaluationFilters],
	);

	const openJob = useCallback(
		async (job: Job) => {
			try {
				const detail = await workspaceClient.job(job.id);
				setSelectedJob(detail);
				setSelectedEvaluationId(null);
				setInvitation(null);
				setRequirements(
					detail.requirements.length
						? detail.requirements.map((requirement) => ({
								...requirement,
								normalizedText:
									requirement.text ??
									requirement.normalizedText,
							}))
						: draftsToRequirements(detail),
				);
				setActiveTab(detail.confirmed ? "results" : "criteria");
			} catch (reason) {
				reportError(reason);
			}
		},
		[reportError],
	);

	useEffect(() => {
		if (!session?.user) return;
		void workspaceClient
			.organizations()
			.then((records) => setOrganization(records[0] ?? null))
			.catch(reportError)
			.finally(() => setOrganizationsLoaded(true));
	}, [session?.user, reportError]);

	useEffect(() => {
		if (!organizationId) return;
		setInvitation(null);
		setSelectedJob(null);
		setSelectedEvaluationId(null);
		void workspaceClient
			.jobs(organizationId)
			.then(async (list) => {
				setJobs(list);
				const firstJob = list[0];
				if (firstJob) await openJob(firstJob);
			})
			.catch(reportError)
			.finally(() => setJobsLoaded(true));
	}, [organizationId, openJob, reportError]);

	const selectedJobId = selectedJob?.id;
	// A confirmed version is locked, so its draft can no longer change even
	// when no extraction result was ever stored for it.
	const isDraftExtracting =
		selectedJob !== null &&
		!selectedJob.confirmed &&
		selectedJob.draftStatus === "processing";

	useEffect(() => {
		if (!selectedJobId) return;
		void refreshEvaluations(selectedJobId).catch(reportError);
	}, [selectedJobId, refreshEvaluations, reportError]);

	useEffect(() => {
		if (!selectedJobId || !isDraftExtracting) return;
		const jobId = selectedJobId;
		const interval = window.setInterval(() => {
			void workspaceClient
				.job(jobId)
				.then((detail) => {
					setSelectedJob((current) =>
						current?.id === jobId ? detail : current,
					);
					if (detail.draftStatus !== "processing") {
						setRequirements((current) =>
							current.length
								? current
								: draftsToRequirements(detail),
						);
					}
				})
				.catch(() => {});
		}, 1_500);
		return () => window.clearInterval(interval);
	}, [selectedJobId, isDraftExtracting]);

	// Terminal states never change, so polling must stop once every
	// evaluation is complete or failed.
	const hasPendingEvaluations = allEvaluations.some(
		(evaluation) =>
			evaluation.status === "pending" ||
			evaluation.status === "processing",
	);

	useEffect(() => {
		if (!selectedJobId || !hasPendingEvaluations) return;
		const jobId = selectedJobId;
		const interval = window.setInterval(() => {
			void refreshEvaluations(jobId).catch(() => {});
		}, 3_000);
		return () => window.clearInterval(interval);
	}, [selectedJobId, hasPendingEvaluations, refreshEvaluations]);

	const overlayOpen =
		isCreateOrgOpen || isCreateJobOpen || isWindowOpen || isMembersOpen;

	useEffect(() => {
		if (!overlayOpen) return;
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key === "Escape") closeOverlays();
		};
		window.addEventListener("keydown", onKeyDown);
		return () => window.removeEventListener("keydown", onKeyDown);
	}, [overlayOpen, closeOverlays]);

	const showDemoResults = useCallback(() => {
		setEligibilityFilter("all");
		setActiveTab("results");
	}, []);
	const showDemoEvidence = useCallback(() => {
		setActiveTab("results");
		const example =
			evaluations.find(
				(evaluation) => evaluation.eligibility === "eligible",
			) ?? evaluations[0];
		if (example) setSelectedEvaluationId(example.id);
	}, [evaluations]);
	const showDemoExport = useCallback(() => {
		setActiveTab("results");
		setIsExportOpen(true);
	}, []);
	useDemoTourAction("employer-results", showDemoResults);
	useDemoTourAction("employer-evidence", showDemoEvidence);
	useDemoTourAction("employer-export", showDemoExport);

	if (isPending) {
		return (
			<main className="workspace-page">
				<p className="empty-state loading-inline" role="status">
					<ThinkingOrb aria-hidden size={20} state="solving" />
					Loading workspace...
				</p>
			</main>
		);
	}

	if (!session?.user) {
		return (
			<main className="workspace-page">
				<section className="auth-page">
					<div className="auth-panel">
						<h1 style={{ fontSize: "1.5rem", fontWeight: 400 }}>
							Sign in to access the employer workspace
						</h1>
						<Button asChild className="w-full">
							<a href="/sign-in">Sign in</a>
						</Button>
					</div>
				</section>
			</main>
		);
	}

	const confirm = async () => {
		if (!selectedJob || isConfirming) return;
		setIsConfirming(true);
		try {
			await workspaceClient.confirmRequirements(
				selectedJob.id,
				requirements,
			);
			const updated = await workspaceClient.job(selectedJob.id);
			setSelectedJob(updated);
			setNotice(
				"Requirements confirmed into a new immutable version. Submissions are scored against it.",
			);
			setActiveTab("results");
		} catch (reason) {
			reportError(reason);
		} finally {
			setIsConfirming(false);
		}
	};

	const upload = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		if (!selectedJob || resumes.length === 0 || isUploading) return;
		setIsUploading(true);
		try {
			const archives = resumes.filter((resume) =>
				resume.name.toLowerCase().endsWith(".zip"),
			);
			const documents = resumes.filter(
				(resume) => !resume.name.toLowerCase().endsWith(".zip"),
			);
			const outcomes = await Promise.allSettled([
				...archives.map((archive) =>
					workspaceClient.uploadResumeBatch(selectedJob.id, archive),
				),
				...(documents.length > 0
					? [
							workspaceClient.uploadResumeFiles(
								selectedJob.id,
								documents,
							),
						]
					: []),
			]);
			const accepted: Array<{ name: string }> = [];
			const rejected: Array<{ name: string; reason: string }> = [];
			for (const outcome of outcomes) {
				if (outcome.status === "fulfilled") {
					accepted.push(...outcome.value.accepted);
					rejected.push(...outcome.value.rejected);
				} else {
					rejected.push({
						name: "Upload",
						reason:
							outcome.reason instanceof Error
								? outcome.reason.message
								: "Upload failed",
					});
				}
			}
			setResumes([]);
			setUploadInputKey((current) => current + 1);
			await refreshEvaluations(selectedJob.id);
			setActiveTab("results");
			setNotice(
				`${accepted.length} resume${accepted.length === 1 ? "" : "s"} queued.`,
			);
			if (rejected.length) {
				setError(
					rejected
						.map(
							(rejection) =>
								`${rejection.name}: ${rejection.reason}`,
						)
						.join(" "),
				);
			}
		} catch (reason) {
			reportError(reason);
		} finally {
			setIsUploading(false);
		}
	};

	const handleCreateInvitation = async () => {
		if (!selectedJob || isCreatingInvitation) return;
		setIsCreatingInvitation(true);
		try {
			const created = await workspaceClient.createInvitation(
				selectedJob.id,
				inviteHours,
			);
			setInvitation({
				token: created.token,
				passcode: created.passcode,
				expiresAt: created.expiresAt,
			});
		} catch (reason) {
			reportError(reason);
		} finally {
			setIsCreatingInvitation(false);
		}
	};

	const copyInvitationLink = () => {
		if (!invitation) return;
		const link = `${window.location.origin}/apply/${invitation.token}`;
		void navigator.clipboard.writeText(link);
		setCopiedInvitation(true);
		setTimeout(() => setCopiedInvitation(false), 2_000);
	};

	const visibleEvaluations = evaluations.filter((evaluation) => {
		const query = evaluationQuery.trim().toLowerCase();
		if (!query) return true;
		return `${evaluation.candidateName ?? ""} ${evaluation.candidateEmail ?? ""}`
			.toLowerCase()
			.includes(query);
	});

	const selectedEvaluation =
		visibleEvaluations.find(
			(evaluation) => evaluation.id === selectedEvaluationId,
		) ??
		visibleEvaluations[0] ??
		null;

	const filteredJobs = jobs.filter((job) =>
		job.title.toLowerCase().includes(jobSearch.toLowerCase()),
	);
	const startCreateJob = () => {
		if (organizationId) setIsCreateJobOpen(true);
		else setIsCreateOrgOpen(true);
	};
	return (
		<div className="workspace-page">
			<header className="workspace-header" data-tour="employer-header">
				<div className="workspace-header-left">
					<a href="/" className="brand-mark">
						<img src="/icon.webp" alt="SkillSignal" />
					</a>
					{organization && (
						<>
							<span aria-hidden className="workspace-org-divider">
								/
							</span>
							<strong className="workspace-org">
								{organization.name}
							</strong>
						</>
					)}
				</div>
				<div className="workspace-header-right">
					<Button
						disabled={!organizationId}
						onClick={() => setIsMembersOpen(true)}
						size="sm"
						variant="ghost"
					>
						Members
					</Button>
					<span className="workspace-user">
						{session.user.name || session.user.email}
					</span>
					<Button
						onClick={() => authClient.signOut()}
						size="sm"
						variant="outline"
					>
						Sign out
					</Button>
				</div>
			</header>

			{notice && (
				<div className="workspace-banner">
					<span>{notice}</span>
					<button
						aria-label="Dismiss notice"
						className="icon-button"
						onClick={() => setNotice(null)}
						type="button"
					>
						<X />
					</button>
				</div>
			)}
			{error && (
				<div className="workspace-banner banner-error">
					<span>{error}</span>
					<button
						aria-label="Dismiss error"
						className="icon-button"
						onClick={() => setError(null)}
						type="button"
					>
						<X />
					</button>
				</div>
			)}

			<div className="workspace-layout">
				<aside className="workspace-aside" data-tour="employer-role">
					<div className="workspace-aside-head">
						<h2>Roles</h2>
						<Button
							onClick={startCreateJob}
							size="sm"
							variant="outline"
						>
							<Plus />
							New
						</Button>
					</div>
					<Input
						aria-label="Search roles"
						onChange={(event) => setJobSearch(event.target.value)}
						placeholder="Search roles"
						value={jobSearch}
					/>
					<nav aria-label="Roles" className="role-list">
						{filteredJobs.map((job) => (
							<button
								aria-current={
									selectedJob?.id === job.id
										? "page"
										: undefined
								}
								className="role-item"
								key={job.id}
								onClick={() => void openJob(job)}
								type="button"
							>
								<span className="role-item-name">
									{job.title}
								</span>
								<span className="role-item-meta">
									{job.confirmed
										? "Requirements confirmed"
										: "Draft criteria"}
								</span>
							</button>
						))}
						{jobsLoaded && filteredJobs.length === 0 && (
							<p className="muted-copy">
								{jobs.length === 0
									? "No roles yet. Create one to start screening."
									: "No roles match this search."}
							</p>
						)}
					</nav>
				</aside>

				<main className="workspace-main">
					{selectedJob ? (
						<div className="workspace-stage">
							<div className="job-head" data-tour="employer-job">
								<div>
									<h1>{selectedJob.title}</h1>
									<p className="job-meta">
										{selectedJob.confirmed
											? "Requirements confirmed"
											: "Requirements need confirmation"}
										<span aria-hidden> · </span>
										{applicationStatus(selectedJob)}
										<span aria-hidden> · </span>
										{requirements.length} criteria
									</p>
								</div>
								<div className="job-actions">
									<ShareMenu
										copied={copiedInvitation}
										invitation={invitation}
										inviteHours={inviteHours}
										isCreating={isCreatingInvitation}
										job={selectedJob}
										onCopyLink={copyInvitationLink}
										onCreateInvitation={() =>
											void handleCreateInvitation()
										}
										onEditWindow={() =>
											setIsWindowOpen(true)
										}
										setInviteHours={setInviteHours}
									/>
									<Button
										onClick={() => setActiveTab("upload")}
										size="sm"
									>
										<UploadCloud />
										Add resumes
									</Button>
								</div>
							</div>

							{activeTab === "upload" ? (
								<button
									className="back-link"
									onClick={() => setActiveTab("results")}
									type="button"
								>
									<ArrowLeft aria-hidden />
									Back to results
								</button>
							) : (
								<nav
									aria-label="Job sections"
									className="job-tabs"
								>
									<button
										aria-current={
											activeTab === "results"
												? "page"
												: undefined
										}
										className="job-tab"
										onClick={() => setActiveTab("results")}
										type="button"
									>
										Results
										<span>{allEvaluations.length}</span>
									</button>
									<button
										aria-current={
											activeTab === "criteria"
												? "page"
												: undefined
										}
										className="job-tab"
										onClick={() => setActiveTab("criteria")}
										type="button"
									>
										Criteria
										<span>{requirements.length}</span>
									</button>
								</nav>
							)}

							{activeTab === "results" &&
								evaluationsJobId !== selectedJob.id && (
									<p
										className="empty-state loading-inline"
										role="status"
									>
										<ThinkingOrb
											aria-hidden
											size={20}
											state="solving"
										/>
										Loading results...
									</p>
								)}
							{activeTab === "results" &&
								evaluationsJobId === selectedJob.id && (
									<ResultsTab
										allEvaluations={allEvaluations}
										eligibilityFilter={eligibilityFilter}
										evaluationQuery={evaluationQuery}
										exportCsv={() => setIsExportOpen(true)}
										minimumScoreText={minimumScoreText}
										onAddResumes={() =>
											setActiveTab("upload")
										}
										onSelect={(evaluation) =>
											setSelectedEvaluationId(
												evaluation.id,
											)
										}
										outcomeFilter={outcomeFilter}
										selectedEvaluation={selectedEvaluation}
										setEligibilityFilter={
											setEligibilityFilter
										}
										setEvaluationQuery={setEvaluationQuery}
										setMinimumScoreText={
											setMinimumScoreText
										}
										setOutcomeFilter={setOutcomeFilter}
										setSkillFilter={setSkillFilter}
										setStatusFilter={setStatusFilter}
										skillFilter={skillFilter}
										statusFilter={statusFilter}
										visibleEvaluations={visibleEvaluations}
									/>
								)}
							{activeTab === "criteria" && (
								<CriteriaTab
									canConfirm={
										requirements.length > 0 &&
										selectedJob.draftStatus !==
											"processing" &&
										!isConfirming
									}
									isConfirming={isConfirming}
									confirmed={selectedJob.confirmed}
									draftError={selectedJob.draftError}
									draftDegraded={selectedJob.draftDegraded}
									draftStatus={selectedJob.draftStatus}
									draftWarnings={selectedJob.draftWarnings}
									onAdd={() =>
										setRequirements((current) => [
											...current,
											{
												stableId: `custom_${Date.now()}`,
												normalizedText: "",
												kind: "required",
												weight: 2,
												category: "other",
												assessability:
													"resume_evidence",
											},
										])
									}
									onConfirm={() => void confirm()}
									onChange={(index, patch) =>
										setRequirements((current) =>
											current.map((item, itemIndex) =>
												itemIndex === index
													? { ...item, ...patch }
													: item,
											),
										)
									}
									requirements={requirements}
								/>
							)}

							{activeTab === "upload" && (
								<UploadTab
									confirmed={selectedJob.confirmed}
									isUploading={isUploading}
									onConfirmCriteria={() =>
										setActiveTab("criteria")
									}
									onSubmit={(event) => void upload(event)}
									resumes={resumes}
									appendResumes={appendResumes}
									uploadInputKey={uploadInputKey}
								/>
							)}
						</div>
					) : !organizationsLoaded ||
						(organizationId && !jobsLoaded) ? (
						<p className="empty-state loading-inline" role="status">
							<ThinkingOrb
								aria-hidden
								size={20}
								state="solving"
							/>
							Loading roles...
						</p>
					) : (
						<div className="empty-state">
							<Briefcase aria-hidden />
							<h3>
								{organizationId
									? "Select a role"
									: "Create your organization"}
							</h3>
							<p>
								{organizationId
									? "Choose a role from the sidebar, or create a new one."
									: "An employer organization owns your roles, submissions, and evaluations."}
							</p>
							<Button onClick={startCreateJob} size="sm">
								<Plus />
								{organizationId
									? "New role"
									: "Create organization"}
							</Button>
						</div>
					)}
				</main>
			</div>

			{isCreateOrgOpen && (
				<CreateOrganizationDialog
					onDismiss={() => setIsCreateOrgOpen(false)}
					onError={reportError}
					onCreated={(created) => {
						closeOverlays();
						setOrganization(created);
						setNotice("Employer organization created.");
					}}
				/>
			)}

			{isCreateJobOpen && (
				<CreateJobDialog
					organizationId={organizationId}
					onDismiss={() => setIsCreateJobOpen(false)}
					onError={reportError}
					onCreated={(detail) => {
						closeOverlays();
						setSelectedJob(detail);
						setRequirements(draftsToRequirements(detail));
						setActiveTab("criteria");
						void workspaceClient
							.jobs(organizationId)
							.then(setJobs)
							.catch(reportError);
						setNotice(
							"Role created. Requirement extraction is running before recruiter review.",
						);
					}}
				/>
			)}

			{isWindowOpen && selectedJob && (
				<ApplicationWindowDialog
					job={selectedJob}
					onDismiss={() => setIsWindowOpen(false)}
					onError={reportError}
					onSaved={(detail) => {
						setIsWindowOpen(false);
						setSelectedJob(detail);
						setNotice("Application window updated.");
					}}
				/>
			)}

			{isMembersOpen && organization && (
				<MembersDialog
					organization={organization}
					onDismiss={() => setIsMembersOpen(false)}
					onNotice={setNotice}
					onError={reportError}
				/>
			)}

			{isExportOpen && selectedJob && (
				<ExportDialog
					jobId={selectedJob.id}
					filters={evaluationFilters}
					onDismiss={() => setIsExportOpen(false)}
					onError={reportError}
				/>
			)}
		</div>
	);
};
