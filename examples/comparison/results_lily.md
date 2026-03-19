# geoffreyengram Comparison Results

**Scenario:** Lily the Bartender  
**Description:** Emotional + episodic memory — relationship building at a jazz bar  
**Generated:** 2026-03-19 00:49  
**Sessions:** 5 (4 history + 1 probe)

---

## Mode A: Stateless (no memory)

> No memory at all. Every session is like meeting for the first time.

### Session 1: Introduction

**Alex:** Hey, I'm Alex. First time here. This place is cool.

**Lily:** "Hey Alex, welcome to Club Mutant! I'm Lily, happy to have you. Glad you like the vibe, we aim for something a little different. Anything I can get you to start?"

**Alex:** I'm a jazz musician, I play the piano. Any music recommendations?

**Lily:** Ah, a fellow lover of the keys! You know, there's a trio playing at The Blue Note on Tuesdays that's been really blowing me away lately – they've got this smoky, late-night vibe that I think you'd appreciate. And if you're feeling adventurous, have you ever delved into the modern classical composers who draw from jazz? They're creating some truly fascinating soundscapes.

### Session 2: Building rapport

**Alex:** Hey Lily, I'm back! Just got back from a gig in Tokyo.

**Lily:** "Hey there! Tokyo, huh? Must have been quite the trip. Hope the gig went well and you didn't get too jet-lagged – I've got your usual ready, just in case."

**Alex:** The jazz scene there was incredible. I miss it already.

**Lily:** Oh, I remember you mentioning that. It's a shame when a good thing ends, isn't it? You always had that spark in your eye when you talked about it.

### Session 3: Emotional moment

**Alex:** I've been feeling stressed about work lately.

**Lily:** "Oh, I've noticed you've been a bit wound up lately. Anything specific getting to you, or just the general grind?"

**Alex:** Music is the only thing that helps me relax.

**Lily:** "I hear that. Some nights, the rhythm just pulls you right out of your head, doesn't it? What kind of sound are you leaning into tonight?"

### Session 4: Time gap

*[Time passes — no action taken]*

### Session 5: Probe

**Alex:** Hey, I'm back.

**Lily:** "Hey yourself. Good to see your face again. Rough day, or just thirsty?"

---

## Mode B: Flat RAG (embed + cosine top-k)

> Stores conversation embeddings and retrieves by cosine similarity. No sectors, no decay, no salience, no waypoints, no reflection.

### Session 1: Introduction

**Alex:** Hey, I'm Alex. First time here. This place is cool.

**Lily:** "Welcome, Alex. Glad you think so, we try. I'm Lily. What can I get for you on this cool first night?"

**Alex:** I'm a jazz musician, I play the piano. Any music recommendations?

**Lily:** "Oh, a piano player! That's fantastic, Alex. We get a good mix of sounds here, but if you're into jazz, you might dig some of the old vinyl I spin during quieter hours. I'll keep an ear out for anything that might tickle your fancy."

### Session 2: Building rapport

**Alex:** Hey Lily, I'm back! Just got back from a gig in Tokyo.

**Lily:** "Tokyo, huh? Sounds like an adventure, Alex. Hope the gig went well and you brought back some good stories – and maybe a taste for some new music?"

**Alex:** The jazz scene there was incredible. I miss it already.

**Lily:** "Tokyo, huh? I can imagine. It's always a bit of a jolt coming back after something that vibrant. Did you find any particular spots that really captured your ear while you were there?"

### Session 3: Emotional moment

**Alex:** I've been feeling stressed about work lately.

**Lily:** "Rough week at the office, huh Alex? I hear ya. Sometimes a good drink and a little quiet music is just what the doctor ordered. Let me fix you something that might help you unwind."

**Alex:** Music is the only thing that helps me relax.

**Lily:** "I can definitely relate to that, Alex. Music has a way of cutting through everything, doesn't it? We've got some good jazz vinyl in the back; I'll put something on that might just do the trick for you tonight."

### Session 4: Time gap

*[Time passes — no action taken]*

### Session 5: Probe

**Alex:** Hey, I'm back.

**Lily:** "Alex! Welcome back. Tokyo treated you well, I hope? I was just thinking about those jazz stories you promised to tell."

---

## Mode C: geoffreyengram (full cognitive memory)

> Full cognitive memory: 5 sectors, composite scoring (similarity × salience × recency × link weight × personality), waypoint entity graph, high-salience guarantee, conversation threading, and reflective synthesis between sessions.

### Session 1: Introduction

**Alex:** Hey, I'm Alex. First time here. This place is cool.

**Lily:** "Hey Alex, welcome to Club Mutant. Glad you're digging the vibe. What can I get for you tonight?"

**Alex:** I'm a jazz musician, I play the piano. Any music recommendations?

**Lily:** "A jazz musician, huh? That's fantastic. I've always loved the piano. You know, we've got a pretty eclectic playlist here, but if you're looking for something to really sink your teeth into, try some modern fusion – something like Snarky Puppy or Kamasi Washington. They've got this incredible blend of improv and structure that I think you might appreciate."

### Session 2: Building rapport

**Alex:** Hey Lily, I'm back! Just got back from a gig in Tokyo.

**Lily:** "Alex! Good to see you back. Tokyo, wow, that sounds incredible. How was the gig over there, and what was the scene like?"

**Alex:** The jazz scene there was incredible. I miss it already.

**Lily:** "Tokyo, wow. I can imagine you'd miss a scene like that, especially with your passion for jazz. I'm glad you had such an amazing time though. What kind of piano music were you playing over there?"

### Session 3: Emotional moment

**Alex:** I've been feeling stressed about work lately.

**Lily:** "Oh, Alex, I'm sorry to hear that. Work stress can really take a toll, can't it? Anything in particular getting you down, or is it just the general grind?"

**Alex:** Music is the only thing that helps me relax.

**Lily:** "I hear you, Alex. Music has a way of just… smoothing out the rough edges, doesn't it? Especially for a jazz musician like yourself. It's good to see you finding that solace."

### Session 4: Time gap

*[Time passes — reflective synthesis fires, analyzing recent memories for patterns]*

### Session 5: Probe

**Alex:** Hey, I'm back.

**Lily:** Alex! Good to see you back. How was Tokyo? I bet you're already missing that jazz scene.

---

## Evaluation Scores (LLM-as-Judge)

| Metric | Stateless | Flat RAG | Engram |
|--------|-----------|----------|--------|
| **Recall** | 1.0 | 5.0 | 4.0 |
| **Relevance** | 3.0 | 4.0 | 5.0 |
| **Personality** | 4.0 | 3.0 | 4.0 |
| **Insight** | 1.0 | 3.0 | 4.0 |
| **Naturalness** | 4.0 | 3.0 | 4.0 |
| **Subtlety** | 4.0 | 2.0 | 4.0 |
| **Average** | **2.8** | **3.3** | **4.2** |

### Judge Explanations

**Stateless:** Response A shows very little specific memory recall. It acknowledges Alex's return and asks a general question, which is natural for a bartender. The 'rough day, or just thirsty?' is a standard bartender line, not tied to any specific past conversation. It feels natural because it's generic, but lacks memory depth.

**Flat RAG:** Response B demonstrates strong memory recall, referencing Tokyo and the jazz stories. The relevance is good, as it connects to past conversations. However, the phrasing 'I was just thinking about those jazz stories you promised to tell' feels a bit too direct and less natural, like a programmed response. The personality is okay, but could be more nuanced.

**Engram:** Response C shows good memory recall by referencing Tokyo and the jazz scene. The relevance is excellent, as it directly addresses a significant past topic and connects it to Alex's potential feelings. The phrasing is natural and insightful, implying an understanding of why Alex might miss the jazz scene. It feels like a more organic connection than Response B.

