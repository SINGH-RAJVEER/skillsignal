import { Button } from "@skillsignal/ui/components/button";
import {
	createContext,
	type ReactNode,
	useCallback,
	useContext,
	useEffect,
	useMemo,
	useRef,
	useState,
} from "react";
import { authClient } from "../../lib/auth-client";
import { DEMO_STEPS, type DemoAct } from "./steps";

type TourState = {
	userId: string;
	accountType: DemoAct;
	act: DemoAct;
	stepIndex: number;
};

type TourContextValue = {
	beginDemo: () => Promise<{ error: string | null }>;
	registerAction: (
		name: string,
		action: () => void | Promise<void>,
	) => () => void;
};

const storageKey = "skillsignal-demo-tour";
const TourContext = createContext<TourContextValue | null>(null);

const readTour = (): TourState | null => {
	try {
		const saved = sessionStorage.getItem(storageKey);
		if (!saved) return null;
		const value = JSON.parse(saved) as TourState;
		if (
			typeof value.userId !== "string" ||
			(value.accountType !== "employer" &&
				value.accountType !== "candidate") ||
			(value.act !== "employer" && value.act !== "candidate") ||
			!Number.isInteger(value.stepIndex) ||
			value.stepIndex < 0 ||
			value.stepIndex >= DEMO_STEPS[value.act].length
		)
			return null;
		return value;
	} catch {
		return null;
	}
};

const saveTour = (tour: TourState | null) => {
	if (tour) sessionStorage.setItem(storageKey, JSON.stringify(tour));
	else sessionStorage.removeItem(storageKey);
};

export const DemoTourProvider = ({ children }: { children: ReactNode }) => {
	const { data: session, isPending } = authClient.useSession();
	const [tour, setTour] = useState<TourState | null>(null);
	const [transitionError, setTransitionError] = useState<string | null>(null);
	const actions = useRef(new Map<string, () => void | Promise<void>>());
	const switchingAct = useRef(false);
	const currentSteps = tour ? DEMO_STEPS[tour.act] : [];
	const currentStep = tour ? currentSteps[tour.stepIndex] : null;

	useEffect(() => {
		if (isPending) return;
		if (!tour) {
			const saved = readTour();
			if (
				saved &&
				session?.user.isDemo &&
				session.user.id === saved.userId &&
				session.user.accountType === saved.accountType
			) {
				setTour(saved);
				return;
			}
			saveTour(null);
			return;
		}
		if (switchingAct.current) return;
		if (
			!session?.user.isDemo ||
			session.user.id !== tour.userId ||
			session.user.accountType !== tour.accountType
		) {
			saveTour(null);
			setTour(null);
		}
	}, [isPending, session, tour]);

	useEffect(() => {
		if (!currentStep) return;
		let cancelled = false;
		let timer: number | undefined;
		let target: HTMLElement | null = null;
		let actionWaits = 0;
		let targetWaits = 0;
		let lastAction: (() => void | Promise<void>) | undefined;
		let isRunningAction = false;
		const maximumActionWaits = 50;
		const maximumTargetWaits = 100;
		const clearTarget = () => {
			document
				.querySelectorAll("[data-demo-tour-active]")
				.forEach((element) => {
					element.removeAttribute("data-demo-tour-active");
				});
		};
		clearTarget();
		const findTarget = () =>
			document.querySelector<HTMLElement>(
				`[data-tour="${currentStep.target}"]`,
			);
		const runAction = (action: () => void | Promise<void>) => {
			lastAction = action;
			isRunningAction = true;
			Promise.resolve()
				.then(action)
				.catch((reason: unknown) => {
					setTransitionError(
						reason instanceof Error
							? reason.message
							: "This demo step could not be opened.",
					);
				})
				.finally(() => {
					isRunningAction = false;
					reveal();
				});
		};
		const reveal = () => {
			if (cancelled) return;
			const latestAction = currentStep.action
				? actions.current.get(currentStep.action)
				: undefined;
			if (
				latestAction &&
				latestAction !== lastAction &&
				!isRunningAction
			) {
				runAction(latestAction);
				return;
			}
			target = findTarget();
			if (target) {
				target.setAttribute("data-demo-tour-active", "true");
				target.scrollIntoView({ block: "center", behavior: "smooth" });
				return;
			}
			if (targetWaits < maximumTargetWaits) {
				targetWaits += 1;
				timer = window.setTimeout(reveal, 100);
			}
		};
		const waitForAction = () => {
			if (cancelled) return;
			const action = currentStep.action
				? actions.current.get(currentStep.action)
				: undefined;
			if (action) {
				runAction(action);
				return;
			}
			if (currentStep.action && actionWaits < maximumActionWaits) {
				actionWaits += 1;
				timer = window.setTimeout(waitForAction, 100);
				return;
			}
			reveal();
		};
		waitForAction();
		return () => {
			cancelled = true;
			if (timer !== undefined) window.clearTimeout(timer);
			target?.removeAttribute("data-demo-tour-active");
			clearTarget();
		};
	}, [currentStep]);

	useEffect(() => {
		if (!tour) return;
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key === "Escape") {
				saveTour(null);
				setTour(null);
			}
		};
		window.addEventListener("keydown", onKeyDown);
		return () => window.removeEventListener("keydown", onKeyDown);
	}, [tour]);

	const beginDemo = useCallback(async () => {
		setTransitionError(null);
		const result = await authClient.startDemo("employer");
		if (result.error) return { error: result.error.message };
		const nextTour: TourState = {
			userId: result.data.user.id,
			accountType: "employer",
			act: "employer",
			stepIndex: 0,
		};
		saveTour(nextTour);
		setTour(nextTour);
		return { error: null };
	}, []);

	const registerAction = useCallback(
		(name: string, action: () => void | Promise<void>) => {
			actions.current.set(name, action);
			return () => {
				if (actions.current.get(name) === action)
					actions.current.delete(name);
			};
		},
		[],
	);

	const contextValue = useMemo(
		() => ({ beginDemo, registerAction }),
		[beginDemo, registerAction],
	);

	const closeTour = () => {
		saveTour(null);
		setTour(null);
	};

	const previousStep = () => {
		if (!tour || tour.stepIndex === 0) return;
		const next = { ...tour, stepIndex: tour.stepIndex - 1 };
		saveTour(next);
		setTour(next);
	};

	const nextStep = async () => {
		if (!tour) return;
		setTransitionError(null);
		if (tour.stepIndex < currentSteps.length - 1) {
			const next = { ...tour, stepIndex: tour.stepIndex + 1 };
			saveTour(next);
			setTour(next);
			return;
		}
		if (tour.act === "candidate") {
			closeTour();
			return;
		}
		switchingAct.current = true;
		const result = await authClient.startDemo("candidate");
		if (result.error) {
			switchingAct.current = false;
			setTransitionError(result.error.message);
			return;
		}
		const next: TourState = {
			userId: result.data.user.id,
			accountType: "candidate",
			act: "candidate",
			stepIndex: 0,
		};
		saveTour(next);
		setTour(next);
		switchingAct.current = false;
	};

	return (
		<TourContext.Provider value={contextValue}>
			{children}
			{tour && currentStep && (
				<section aria-label="Guided demo" className="demo-tour-panel">
					<div className="demo-tour-copy">
						<p className="eyebrow">
							{tour.act === "employer"
								? "Employer tour"
								: "Candidate tour"}{" "}
							· {tour.stepIndex + 1} of {currentSteps.length}
						</p>
						<h2>{currentStep.title}</h2>
						<p>{currentStep.body}</p>
						{transitionError && (
							<p className="form-error" role="alert">
								{transitionError}
							</p>
						)}
					</div>
					<div className="demo-tour-actions">
						<Button onClick={closeTour} size="sm" variant="ghost">
							Close tour
						</Button>
						<Button
							disabled={tour.stepIndex === 0}
							onClick={previousStep}
							size="sm"
							variant="outline"
						>
							Back
						</Button>
						<Button onClick={() => void nextStep()} size="sm">
							{tour.act === "employer" &&
							tour.stepIndex === currentSteps.length - 1
								? "Continue as candidate"
								: tour.act === "candidate" &&
										tour.stepIndex ===
											currentSteps.length - 1
									? "Finish"
									: "Next"}
						</Button>
					</div>
				</section>
			)}
		</TourContext.Provider>
	);
};

export const useDemoTour = () => {
	const context = useContext(TourContext);
	if (!context)
		throw new Error("useDemoTour must be used within DemoTourProvider");
	return context;
};

export const useDemoTourAction = (
	name: string,
	action: () => void | Promise<void>,
) => {
	const { registerAction } = useDemoTour();
	useEffect(
		() => registerAction(name, action),
		[action, name, registerAction],
	);
};
