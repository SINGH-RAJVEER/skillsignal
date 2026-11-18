import { Button } from "@skillsignal/ui/components/button";
import { useCallback, useState } from "react";
import { CandidateSubmissionWorkspace } from "../features/candidate/CandidateSubmissionWorkspace";
import {
	type CandidateView,
	PrivateEvaluationWorkspace,
} from "../features/candidate/PrivateEvaluationWorkspace";
import { useDemoTourAction } from "../features/tour/DemoTour";
import { authClient } from "../lib/auth-client";

const VIEWS: Array<{ value: CandidateView; label: string }> = [
	{ value: "check", label: "Check" },
	{ value: "history", label: "History" },
	{ value: "submissions", label: "Submissions" },
];

export const CandidateHome = () => {
	const { data: session } = authClient.useSession();
	const [view, setView] = useState<CandidateView>("check");
	const showReceipts = useCallback(() => setView("submissions"), []);
	useDemoTourAction("candidate-receipts", showReceipts);

	return (
		<main className="candidate-page">
			<header className="candidate-header">
				<div className="candidate-header-left">
					<div className="brand-mark">
						<img src="/icon.webp" alt="SkillSignal" />
					</div>
					<nav
						aria-label="Candidate sections"
						className="candidate-nav"
					>
						{VIEWS.map((item) => (
							<button
								aria-current={
									view === item.value ? "page" : undefined
								}
								key={item.value}
								onClick={() => setView(item.value)}
								type="button"
							>
								{item.label}
							</button>
						))}
					</nav>
				</div>
				<div className="candidate-header-right">
					<span>{session?.user.name}</span>
					<Button
						onClick={() => authClient.signOut()}
						size="sm"
						variant="outline"
					>
						Sign out
					</Button>
				</div>
			</header>

			<div className="candidate-content">
				{view === "submissions" ? (
					<CandidateSubmissionWorkspace />
				) : (
					<PrivateEvaluationWorkspace
						onViewChange={setView}
						view={view}
					/>
				)}
			</div>
		</main>
	);
};
