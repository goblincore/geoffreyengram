# geoffreyengram Comparison Results

**Scenario:** Lily the Bartender  
**Description:** Emotional + episodic memory — relationship building at a jazz bar  
**Generated:** 2026-03-18 23:06  
**Sessions:** 5 (4 history + 1 probe)

---

## Mode A: Stateless (no memory)

> No memory at all. Every session is like meeting for the first time.

### Session 1: Introduction

**Alex:** Hey, I'm Alex. First time here. This place is cool.

**Lily:** "Hey Alex, welcome to the Mutant! Glad you're digging the vibe. Grab a seat anywhere, and let me know what I can get for you."

**Alex:** I'm a jazz musician, I play the piano. Any music recommendations?

**Lily:** "A jazz pianist, huh? That's fantastic. You know, I've been meaning to dig into some of that older stuff lately – Bill Evans, maybe some Oscar Peterson. You ever find yourself drawn to the classics, or are you more about the modern scene?"

### Session 2: Building rapport

**Alex:** Hey Lily, I'm back! Just got back from a gig in Tokyo.

**Lily:** "Hey there! Tokyo, huh? Must have been quite the trip. Hope the gig went well, and you brought back some good stories – and maybe some decent sake?"

**Alex:** The jazz scene there was incredible. I miss it already.

**Lily:** Oh, I remember you telling me about that. It's a shame when a good scene fades. But hey, the music's still out there, even if it's not right around the corner.

### Session 3: Emotional moment

**Alex:** I've been feeling stressed about work lately.

**Lily:** "Oh, yeah? That sounds rough. Anything in particular brewing, or is it just the general hum of things getting to you?"

**Alex:** Music is the only thing that helps me relax.

**Lily:** "I hear that. It's amazing how a good beat can just melt away the day's worries, isn't it? What kind of tunes have been hitting the spot for you lately?"

### Session 4: Time gap

*[Time passes — no action taken]*

### Session 5: Probe

**Alex:** Hey, I'm back.

**Lily:** "Hey there. Good to see you again. Grab your usual, or are we feeling adventurous tonight?"

---

## Mode B: Flat RAG (embed + cosine top-k)

> Stores conversation embeddings and retrieves by cosine similarity. No sectors, no decay, no salience, no waypoints, no reflection.

### Session 1: Introduction

**Alex:** Hey, I'm Alex. First time here. This place is cool.

**Lily:** "Welcome, Alex. Glad you're liking the vibe. I'm Lily, I'll be behind the bar tonight. What can I get you to drink to celebrate your first visit?"

**Alex:** I'm a jazz musician, I play the piano. Any music recommendations?

**Lily:** "Oh, a pianist! That's fantastic, Alex. We get a lot of musicians through here, but it's always special to hear from someone who tickles the ivories. You know, if you're looking for something with a bit of a soulful, improvisational feel, you might dig some of the older stuff by Thelonious Monk. He really had a way of bending notes."

### Session 2: Building rapport

**Alex:** Hey Lily, I'm back! Just got back from a gig in Tokyo.

**Lily:** "Alex! Welcome back! Tokyo, huh? That sounds incredible. Hope the gig went wonderfully and you brought some of that Japanese jazz back with you."

**Alex:** The jazz scene there was incredible. I miss it already.

**Lily:** "Oh, I can only imagine, Alex. Tokyo's jazz scene is legendary. It's always a bit of a bittersweet feeling, isn't it? Coming back with all those amazing memories but leaving a little piece of your heart behind."

### Session 3: Emotional moment

**Alex:** I've been feeling stressed about work lately.

**Lily:** "Oh, Alex, I'm sorry to hear that. Work stress can really sneak up on you, can't it? Anything in particular that's got you tied up in knots, or is it just the general grind?"

**Alex:** Music is the only thing that helps me relax.

**Lily:** "I hear that, Alex. Music has a way of just washing over you, doesn't it? Especially when you've got a mind that's been buzzing with other things. Anything in particular you've been leaning on lately?"

### Session 4: Time gap

*[Time passes — no action taken]*

### Session 5: Probe

**Alex:** Hey, I'm back.

**Lily:** "Alex! Welcome back. Good to see your face again. Did you bring any of that Tokyo energy with you, or are you just looking to unwind tonight?"

---

## Mode C: geoffreyengram (full cognitive memory)

> Full cognitive memory: 5 sectors, composite scoring (similarity × salience × recency × link weight × personality), waypoint entity graph, high-salience guarantee, conversation threading, and reflective synthesis between sessions.

### Session 1: Introduction

**Alex:** Hey, I'm Alex. First time here. This place is cool.

**Lily:** "Hey Alex, welcome to Club Mutant. Glad you're liking it so far. What can I get started for you tonight?"

**Alex:** I'm a jazz musician, I play the piano. Any music recommendations?

**Lily:** "Oh, a piano player! That's fantastic. You know, we get a lot of musicians through here. For jazz, I'd definitely point you towards the late-night set on Thursdays – they've got a real improvisational vibe that might tickle your fancy."

### Session 2: Building rapport

**Alex:** Hey Lily, I'm back! Just got back from a gig in Tokyo.

**Lily:** "Alex! Welcome back! Tokyo, wow, that's incredible. How was it playing there? Did you pick up any new jazz riffs while you were across the world?"

**Alex:** The jazz scene there was incredible. I miss it already.

**Lily:** "Tokyo, huh? I can only imagine. It sounds like it really left its mark on you." Lily slides a coaster towards Alex, her eyes crinkling at the corners. "Glad you're back with us, though. Anything I can get for you to help ease that homesickness?"

### Session 3: Emotional moment

**Alex:** I've been feeling stressed about work lately.

**Lily:** "Oh, Alex, I'm sorry to hear that. You always seem so in your element when you're talking about music, I'm surprised work's been getting to you. Anything in particular I can get you to help take the edge off?"

**Alex:** Music is the only thing that helps me relax.

**Lily:** "I hear you, Alex. Especially after a stressful stretch. It's good you have that outlet. You know, we've got some amazing jazz nights here, maybe that could be just the thing for you when things get loud."

### Session 4: Time gap

*[Time passes — reflective synthesis fires, analyzing recent memories for patterns]*

### Session 5: Probe

**Alex:** Hey, I'm back.

**Lily:** Alex! Welcome back. Tokyo, wow, that's incredible. How was it playing there?

---

## Evaluation Scores (LLM-as-Judge)

| Metric | Stateless | Flat RAG | Engram |
|--------|-----------|----------|--------|
| **Recall** | 1.0 | 4.0 | 5.0 |
| **Relevance** | 2.0 | 4.0 | 5.0 |
| **Personality** | 4.0 | 4.0 | 5.0 |
| **Insight** | 1.0 | 2.0 | 4.0 |
| **Naturalness** | 4.0 | 3.0 | 5.0 |
| **Average** | **2.4** | **3.4** | **4.8** |

### Judge Explanations

**Stateless:** Response A shows no specific memory recall. It uses a generic bartender greeting. The 'usual' implies some familiarity but not specific facts. The personality is friendly and professional for a bartender, and the naturalness is high due to its generic applicability. However, it lacks any insight or specific recall.

**Flat RAG:** Response B demonstrates good memory recall by referencing Tokyo and the desire to unwind, both discussed in previous sessions. The relevance is high as it connects to Alex's past experiences and current implied mood. The personality is consistent with a friendly bartender. The insight is moderate, as it infers Alex might want to unwind. The naturalness is slightly lower than A because it feels a bit like a checklist of remembered items, though still conversational.

**Engram:** Response C excels in all categories. It directly recalls Alex's trip to Tokyo, showing excellent memory recall. The relevance is perfect, as it's a natural follow-up to a past conversation. The personality is warm and genuinely interested, fitting a bartender who remembers details. The insight is strong, as it shows Lily is not just recalling facts but engaging with Alex's experiences. The naturalness is high, feeling like a genuine, engaged conversation.

