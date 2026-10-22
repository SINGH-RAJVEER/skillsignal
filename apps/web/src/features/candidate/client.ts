import { downloadFile, apiRequest as request } from "../../lib/api-client";
import type {
	OrderResponse,
	PointPack,
	PointsSummary,
} from "../../lib/billing-types";

export type { OrderResponse, PointPack, PointsSummary };

export const candidateClient = {
	createIndependentEvaluation: (
		file: File,
		jobDescription: string,
		jobDescriptionFile?: File | null,
	) => {
		const body = new FormData();
		body.append("file", file);
		body.append("job_description", jobDescription);
		if (jobDescriptionFile)
			body.append("job_description_file", jobDescriptionFile);
		return request<{ id: string; processingJobId: string }>(
			"/api/independent-evaluations",
			{ method: "POST", body },
		);
	},
	independentEvaluation: (evaluationId: string) =>
		independentEvaluation(evaluationId),
	downloadImprovedResume: (evaluationId: string) =>
		downloadFile(
			`/api/independent-evaluations/${evaluationId}/improved-resume`,
			"corrected-resume.docx",
			"Corrected resume is not available",
		),
	independentEvaluations: () => independentEvaluations(),
	deleteIndependentEvaluation: (evaluationId: string) =>
		request<void>(`/api/independent-evaluations/${evaluationId}`, {
			method: "DELETE",
		}),
	redeemInvitation: (token: string) =>
		request<{ jobId: string; invitationId: string }>(
			`/api/invitations/${token}/redeem`,
			{ method: "POST" },
		),
	redeemPasscode: (passcode: string) =>
		request<{ jobId: string; invitationId: string }>(
			"/api/invitations/redeem",
			{
				method: "POST",
				body: JSON.stringify({ passcode }),
			},
		),
	points: () => request<PointsSummary>("/api/me/points"),
	packs: () => request<PointPack[]>("/api/billing/packs"),
	quote: () =>
		request<PointQuote>("/api/billing/quote?kind=independent_evaluation"),
	createOrder: (packId: string) =>
		request<OrderResponse>("/api/billing/orders", {
			method: "POST",
			body: JSON.stringify({ pack_id: packId }),
		}),
	verifyCheckout: (
		orderId: string,
		razorpayPaymentId: string,
		razorpaySignature: string,
	) =>
		request<{ verified: boolean }>(
			`/api/billing/orders/${orderId}/verify`,
			{
				method: "POST",
				body: JSON.stringify({
					razorpay_payment_id: razorpayPaymentId,
					razorpay_signature: razorpaySignature,
				}),
			},
		),
	uploadInvitedResume: (jobId: string, token: string, file: File) => {
		const body = new FormData();
		body.append("file", file);
		body.append("invitation_token", token);
		return request<{
			submissionId: string;
			receivedAt: string;
		}>(`/api/jobs/${jobId}/resumes`, { method: "POST", body });
	},
};

export type PointQuote = {
	kind: string;
	points: number;
	minimumPoints: number;
	costCeilingPoints: number;
	lineItems: Array<{
		task: string;
		maxInputTokens: number;
		maxOutputTokens: number;
	}>;
};

export type SkillFact = {
	canonicalName: string;
	category?: string | null;
};

export type EmploymentFact = {
	employer?: string | null;
	title?: string | null;
	startDate?: string | null;
	endDate?: string | null;
	isCurrent?: boolean;
};

export type EducationFact = {
	institution?: string | null;
	degree?: string | null;
	fieldOfStudy?: string | null;
	graduationDate?: string | null;
};

export type CertificationFact = { name: string; issuer?: string | null };

export type IndependentEvaluation = {
	id: string;
	originalName: string;
	status: "queued" | "processing" | "complete" | "failed";
	score: number | null;
	generalQualityScore?: number | null;
	evidenceCoverage?: number | null;
	requirementAssessments?: Array<{
		requirementId: string;
		requirement: string;
		outcome: "met" | "partial" | "not_met" | "unknown";
		confidence: number;
		reasoning: string;
		evidence: Array<{ blockId: string; quote: string }>;
	}>;
	safeError: string | null;
	createdAt: string;
	completedAt: string | null;
	jobDescriptionProvided?: boolean;
	hasImprovedResume?: boolean;
	requirements?: Array<{
		stableId?: string;
		id?: string;
		normalizedText?: string;
		text?: string;
	}>;
	suggestions?: Array<{ title: string; detail: string }>;
	facts?: {
		skills?: SkillFact[];
		employment?: EmploymentFact[];
		education?: EducationFact[];
		certifications?: CertificationFact[];
		warnings?: string[];
	};
};

const normalizeIndependentEvaluation = (
	evaluation: IndependentEvaluation,
): IndependentEvaluation => {
	const requirements = new Map(
		(evaluation.requirements ?? []).map((requirement) => [
			requirement.stableId ?? requirement.id ?? "",
			requirement.normalizedText ?? requirement.text ?? "",
		]),
	);
	return {
		...evaluation,
		score: evaluation.jobDescriptionProvided
			? evaluation.score
			: (evaluation.generalQualityScore ?? evaluation.score),
		requirementAssessments: (evaluation.requirementAssessments ?? []).map(
			(assessment, index) => ({
				...assessment,
				requirement:
					assessment.requirement ||
					requirements.get(assessment.requirementId) ||
					evaluation.requirements?.[index]?.normalizedText ||
					evaluation.requirements?.[index]?.text ||
					assessment.requirementId,
				evidence: assessment.evidence ?? [],
			}),
		),
	};
};

const independentEvaluation = async (evaluationId: string) =>
	normalizeIndependentEvaluation(
		await request<IndependentEvaluation>(
			`/api/independent-evaluations/${evaluationId}`,
		),
	);

const independentEvaluations = async () =>
	(
		await request<IndependentEvaluation[]>("/api/independent-evaluations")
	).map(normalizeIndependentEvaluation);
