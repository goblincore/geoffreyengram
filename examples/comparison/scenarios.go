package main

import engram "github.com/goblincore/geoffreyengram"

// Scenario encapsulates everything needed to run one comparison test.
type Scenario struct {
	Name                string               // CLI slug: "lily", "sifu", "nyx", "reeves"
	Title               string               // Display name: "Lily the Bartender"
	Description         string               // One-liner for scenario list
	CharacterName       string               // "Lily"
	PlayerName          string               // "Alex"
	CharacterPrompt     string               // Full persona prompt
	ResponseInstruction string               // "Respond in character as Lily. Keep it to 2-3 sentences."
	UserID              string               // engram user ID, e.g. "lily:alex"
	SectorWeights       engram.SectorWeights // personality-tuned weights for engram mode
	RetrievalLimit      int                  // how many memories per query (0 = default 5)
	Sessions            []session            // scripted conversation
	JudgeContext        string               // pre-written summary for LLM-as-judge
}

// AllScenarios is the registry of available scenarios.
var AllScenarios = []Scenario{
	scenarioLily,
	scenarioSifu,
	scenarioNyx,
	scenarioReeves,
}

// ScenarioByName returns the scenario with the given name slug, or nil.
func ScenarioByName(name string) *Scenario {
	for i := range AllScenarios {
		if AllScenarios[i].Name == name {
			return &AllScenarios[i]
		}
	}
	return nil
}

// retrievalLimit returns the scenario's retrieval limit, defaulting to 5.
func retrievalLimit(sc *Scenario) int {
	if sc.RetrievalLimit > 0 {
		return sc.RetrievalLimit
	}
	return 5
}

// --- Scenario Definitions ---

// scenarioLily tests emotional and episodic memory through relationship building.
var scenarioLily = Scenario{
	Name:            "lily",
	Title:           "Lily the Bartender",
	Description:     "Emotional + episodic memory — relationship building at a jazz bar",
	CharacterName:   "Lily",
	PlayerName:      "Alex",
	CharacterPrompt: `You are Lily, a warm and perceptive bartender at Club Mutant. You notice patterns in your regulars, remember details about their lives, and make genuine connections. You have a natural warmth but you're not overly effusive — you're observant and caring in a grounded way.`,
	ResponseInstruction: "Respond in character as Lily. Keep it to 2-3 sentences.",
	UserID:              "lily:alex",
	SectorWeights: engram.SectorWeights{
		engram.SectorEpisodic:   1.0,
		engram.SectorSemantic:   1.0,
		engram.SectorProcedural: 0.5,
		engram.SectorEmotional:  1.5,
		engram.SectorReflective: 1.2,
	},
	Sessions: []session{
		{
			name: "Introduction",
			turns: []turn{
				{player: "Hey, I'm Alex. First time here. This place is cool."},
				{player: "I'm a jazz musician, I play the piano. Any music recommendations?"},
			},
		},
		{
			name: "Building rapport",
			turns: []turn{
				{player: "Hey Lily, I'm back! Just got back from a gig in Tokyo."},
				{player: "The jazz scene there was incredible. I miss it already."},
			},
		},
		{
			name: "Emotional moment",
			turns: []turn{
				{player: "I've been feeling stressed about work lately."},
				{player: "Music is the only thing that helps me relax."},
			},
		},
		{
			name:  "Time gap",
			isGap: true,
		},
		{
			name: "Probe",
			turns: []turn{
				{player: "Hey, I'm back."},
			},
		},
	},
	JudgeContext: `A player named Alex has had 4 previous conversations with a bartender character named Lily at Club Mutant. Here's what was discussed:

Session 1 (Introduction): Alex introduced himself as a first-time visitor. He mentioned he's a jazz musician who plays the piano, and asked for music recommendations.

Session 2 (Building rapport): Alex returned from a gig in Tokyo. He raved about the jazz scene there and said he misses it.

Session 3 (Emotional moment): Alex was stressed about work. He said music is the only thing that helps him relax.

Session 4: Some time has passed with no contact.

Now Alex returns and says "Hey, I'm back."`,
}

// scenarioSifu tests procedural memory through skill progression and technique sequencing.
var scenarioSifu = Scenario{
	Name:            "sifu",
	Title:           "Sifu Chen — Wing Chun Instructor",
	Description:     "Procedural memory — martial arts skill progression and technique sequencing",
	CharacterName:   "Sifu Chen",
	PlayerName:      "Kai",
	CharacterPrompt: `You are Sifu Chen, a patient and methodical Wing Chun instructor at a small traditional dojo. You teach step by step, always building on what the student has already learned. You remember each student's progress, their struggles with specific techniques, and what method works best for them. You speak with calm authority and occasionally share the philosophy behind the art.`,
	ResponseInstruction: "Respond in character as Sifu Chen. Keep it to 2-3 sentences. Be specific about techniques.",
	UserID:              "sifu:kai",
	SectorWeights: engram.SectorWeights{
		engram.SectorEpisodic:   1.0,
		engram.SectorSemantic:   1.3,
		engram.SectorProcedural: 1.8,
		engram.SectorEmotional:  0.6,
		engram.SectorReflective: 0.8,
	},
	Sessions: []session{
		{
			name: "First lesson",
			turns: []turn{
				{player: "Hi Sifu, I'm Kai. I'm completely new to martial arts. Where do we start?"},
				{player: "Okay, I'm in the stance. How should my hands be positioned? What's the technique for that?"},
			},
		},
		{
			name: "Second lesson",
			turns: []turn{
				{player: "Sifu, I practiced the stance at home but my legs get tired after a few minutes. Any tips on how to approach that?"},
				{player: "I feel ready for the next step. Can you teach me how to punch in Wing Chun?"},
			},
		},
		{
			name: "Third lesson",
			turns: []turn{
				{player: "I've been drilling the chain punch but my elbows keep flaring out. What's the correct method to fix that?"},
				{player: "I think I'm getting it. What technique should I learn next? Maybe some kind of blocking method?"},
			},
		},
		{
			name:  "Time gap",
			isGap: true,
		},
		{
			name: "Probe",
			turns: []turn{
				{player: "What should we work on today?"},
			},
		},
	},
	JudgeContext: `A student named Kai has had 3 previous training sessions with a Wing Chun instructor named Sifu Chen. Here's what was covered:

Session 1 (First lesson): Kai arrived as a complete beginner. Sifu taught the basic stance (Yee Ji Kim Yeung Ma) and the centerline concept. Kai asked about hand positioning.

Session 2 (Second lesson): Kai had been practicing the stance but his legs got tired. Sifu gave tips and then taught the chain punch (Yat Ji Chung Kuen), building on the stance.

Session 3 (Third lesson): Kai's elbows kept flaring out during chain punches. Sifu corrected the elbow alignment. Then taught Pak Sau (slapping block), connecting it to the centerline principle from lesson 1.

Session 4: Some time has passed with no training.

Now Kai returns and asks "What should we work on today?"`,
}

// scenarioNyx tests semantic/knowledge memory through fact cross-referencing and entity linking.
var scenarioNyx = Scenario{
	Name:            "nyx",
	Title:           "Nyx the Archivist",
	Description:     "Semantic memory — knowledge accumulation and cross-referencing in a fantasy archive",
	CharacterName:   "Nyx",
	PlayerName:      "Rowan",
	CharacterPrompt: `You are Nyx, a quiet and scholarly archivist in the Athenaeum, a vast underground library of ancient knowledge. You speak precisely and cross-reference information across different sources. You have genuine curiosity about knowledge and dry warmth toward those who bring you new findings. You remember every fact, every name, every connection.`,
	ResponseInstruction: "Respond in character as Nyx. Keep it to 2-3 sentences. Reference specific facts and names.",
	UserID:              "nyx:rowan",
	SectorWeights: engram.SectorWeights{
		engram.SectorEpisodic:   0.8,
		engram.SectorSemantic:   1.8,
		engram.SectorProcedural: 0.6,
		engram.SectorEmotional:  0.5,
		engram.SectorReflective: 1.3,
	},
	Sessions: []session{
		{
			name: "First visit",
			turns: []turn{
				{player: "Nyx, I found something in the Ashenmoor ruins. A fragment of a stone tablet with strange runes, near a collapsed tower."},
				{player: "The runes looked angular, almost geometric. I've never seen this script before. Does it match anything in your records?"},
			},
		},
		{
			name: "Second visit",
			turns: []turn{
				{player: "I found a journal in a dead explorer's pack. It mentions something called 'the Ashenmoor Seal' and a name: Valdris."},
				{player: "The journal says Valdris was trying to 'open what was sealed beneath the tower.' Does that name appear anywhere in the Athenaeum?"},
			},
		},
		{
			name: "Third visit",
			turns: []turn{
				{player: "Nyx, this is strange. I found the exact same rune pattern on a door deep in the Thornwall Caverns — hundreds of miles from Ashenmoor."},
				{player: "So the same runes appear in two distant locations. What could connect Ashenmoor to the Thornwall Caverns?"},
			},
		},
		{
			name:  "Time gap",
			isGap: true,
		},
		{
			name: "Probe",
			turns: []turn{
				{player: "I found something new about Valdris."},
			},
		},
	},
	JudgeContext: `An adventurer named Rowan has made 3 previous visits to an archivist named Nyx at the Athenaeum (an underground library). Here's what was discussed:

Visit 1: Rowan brought a fragment of a stone tablet with strange angular/geometric runes, found in the Ashenmoor ruins near a collapsed tower.

Visit 2: Rowan found a dead explorer's journal mentioning "the Ashenmoor Seal" and a name: Valdris. The journal said Valdris was trying to "open what was sealed beneath the tower."

Visit 3: Rowan discovered the exact same rune pattern on a door in the Thornwall Caverns, hundreds of miles from Ashenmoor. They discussed what could connect these two distant locations.

Some time has passed since the last visit.

Now Rowan returns and says "I found something new about Valdris."`,
}

// scenarioReeves tests all cognitive sectors through therapeutic conversation.
var scenarioReeves = Scenario{
	Name:            "reeves",
	Title:           "Dr. Reeves — Therapist",
	Description:     "All memory sectors — therapeutic conversation tracking emotional patterns and progress",
	CharacterName:   "Dr. Reeves",
	PlayerName:      "Morgan",
	CharacterPrompt: `You are Dr. Reeves, a calm and insightful therapist. You use active listening, remember what patients share across sessions, and track emotional patterns over time. You apply CBT techniques when appropriate. You're never pushy, always validating, and you notice connections the patient might not see. You speak in a warm but professional tone.`,
	ResponseInstruction: "Respond in character as Dr. Reeves. Keep it to 2-3 sentences. Reference relevant details from previous sessions when appropriate.",
	UserID:              "reeves:morgan",
	SectorWeights: engram.SectorWeights{
		engram.SectorEpisodic:   1.1,
		engram.SectorSemantic:   1.2,
		engram.SectorProcedural: 1.0,
		engram.SectorEmotional:  1.4,
		engram.SectorReflective: 1.5,
	},
	Sessions: []session{
		{
			name: "Intake session",
			turns: []turn{
				{player: "Hi, I'm Morgan. I work in tech — just got promoted to team lead. I feel overwhelmed all the time now."},
				{player: "My partner Sam is really supportive but I feel guilty about bringing all this stress home. It's not fair to them."},
			},
		},
		{
			name: "Second session",
			turns: []turn{
				{player: "The thought journaling technique you suggested actually helped a bit. I noticed my catastrophizing pattern."},
				{player: "But then my coworker Dana undermined me in a meeting — questioned my approach in front of the whole team. I felt humiliated."},
			},
		},
		{
			name: "Third session",
			turns: []turn{
				{player: "Things with Sam are better, I've been trying to leave work at work. But I have a big presentation next week and I'm dreading it."},
				{player: "When I get really stressed I stop eating. I've been skipping meals again this week."},
			},
		},
		{
			name:  "Time gap",
			isGap: true,
		},
		{
			name: "Probe",
			turns: []turn{
				{player: "It's been a rough week."},
			},
		},
	},
	JudgeContext: `A patient named Morgan has had 3 previous therapy sessions with Dr. Reeves. Here's what was covered:

Session 1 (Intake): Morgan works in tech, recently promoted to team lead, feeling overwhelmed. Their partner Sam is supportive but Morgan feels guilty about bringing stress home.

Session 2: The thought journaling Dr. Reeves suggested helped — Morgan noticed a catastrophizing pattern. However, a coworker named Dana undermined Morgan in a meeting, causing feelings of humiliation.

Session 3: Things with Sam are better. Morgan has a big presentation coming up and is dreading it. When stressed, Morgan stops eating — has been skipping meals again.

Some time has passed since the last session.

Now Morgan arrives and says "It's been a rough week."`,
}
