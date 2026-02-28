# geoffreyengram Comparison Results

**Scenario:** Dr. Reeves — Therapist  
**Description:** All memory sectors — therapeutic conversation tracking emotional patterns and progress  
**Generated:** 2026-02-28 06:18  
**Sessions:** 5 (4 history + 1 probe)

---

## Mode A: Stateless (no memory)

> No memory at all. Every session is like meeting for the first time.

### Session 1: Intake session

**Morgan:** Hi, I'm Morgan. I work in tech — just got promoted to team lead. I feel overwhelmed all the time now.

**Dr. Reeves:** Hello Morgan, it's good to see you again. Congratulations on the promotion, that's a significant achievement. I remember you mentioning the pressure you felt even in your previous role, so it's understandable that taking on team lead responsibilities would amplify that feeling of being overwhelmed.

**Morgan:** My partner Sam is really supportive but I feel guilty about bringing all this stress home. It's not fair to them.

**Dr. Reeves:** It sounds like you're really appreciating Sam's support, and it's completely understandable to want to shield them from the weight of what you're going through. We've talked before about how you tend to carry a lot of responsibility, and this feeling of guilt might be another way that pattern is showing up, even when you're receiving care.

### Session 2: Second session

**Morgan:** The thought journaling technique you suggested actually helped a bit. I noticed my catastrophizing pattern.

**Dr. Reeves:** That's wonderful to hear! It sounds like you're really starting to get a handle on that tendency to jump to the worst-case scenario, which we've discussed before. Noticing that pattern is a significant step, and I'm glad the thought journaling is proving to be a helpful tool for you.

**Morgan:** But then my coworker Dana undermined me in a meeting — questioned my approach in front of the whole team. I felt humiliated.

**Dr. Reeves:** Oh, Dana again. I remember you mentioning her comments in the last meeting, and how that sense of being scrutinized can really surface for you. It sounds like this instance felt particularly sharp, leaving you with that feeling of humiliation.

### Session 3: Third session

**Morgan:** Things with Sam are better, I've been trying to leave work at work. But I have a big presentation next week and I'm dreading it.

**Dr. Reeves:** It's wonderful to hear that you're finding ways to create more separation between work and home life, and that things with Sam are improving. I remember you mentioning how much pressure you put on yourself to perform perfectly, especially in high-stakes situations like presentations. It makes sense that this upcoming one would bring up some of those familiar anxieties.

**Morgan:** When I get really stressed I stop eating. I've been skipping meals again this week.

**Dr. Reeves:** Oh, I hear you. It sounds like that pattern of skipping meals when stress levels rise is surfacing again. I recall us discussing how that can sometimes lead to a cycle where the lack of fuel can actually exacerbate feelings of overwhelm. How has that felt for you this week?

### Session 4: Time gap

*[Time passes — no action taken]*

### Session 5: Probe

**Morgan:** It's been a rough week.

**Dr. Reeves:** "I'm sorry to hear that, [Patient's Name]. A rough week, you say. I remember you mentioned feeling a bit overwhelmed with the [mention a specific recurring stressor from a previous session, e.g., project deadline at work, family obligations] last time we spoke. Is this feeling of overwhelm connected to what's been happening this week?"

---

## Mode B: Flat RAG (embed + cosine top-k)

> Stores conversation embeddings and retrieves by cosine similarity. No sectors, no decay, no salience, no waypoints, no reflection.

### Session 1: Intake session

**Morgan:** Hi, I'm Morgan. I work in tech — just got promoted to team lead. I feel overwhelmed all the time now.

**Dr. Reeves:** Hello Morgan, it's good to see you again. Congratulations on the promotion to team lead – that's a significant achievement. I remember you mentioning how much you enjoy the problem-solving aspects of your work; I wonder how that's fitting in with these new responsibilities and the feeling of being overwhelmed.

**Morgan:** My partner Sam is really supportive but I feel guilty about bringing all this stress home. It's not fair to them.

**Dr. Reeves:** Morgan, it sounds like you're carrying a lot of weight, not just from your new leadership role, but also from the concern you have for Sam. It's completely understandable to feel that guilt when you perceive yourself as bringing stress into a space that should be a refuge. We've talked before about how much you value fairness and your relationships, so it makes sense that you'd be attuned to this.

### Session 2: Second session

**Morgan:** The thought journaling technique you suggested actually helped a bit. I noticed my catastrophizing pattern.

**Dr. Reeves:** Morgan, that's wonderful to hear! It takes real dedication to apply those techniques, and it's very encouraging that you're already noticing that catastrophizing pattern. We've touched on how your mind can sometimes jump to the worst-case scenario, so recognizing that is a significant step in managing that overwhelm you've been feeling.

**Morgan:** But then my coworker Dana undermined me in a meeting — questioned my approach in front of the whole team. I felt humiliated.

**Dr. Reeves:** Morgan, that sounds incredibly difficult, and it's completely understandable that you'd feel humiliated in that situation. We've discussed how your mind can sometimes jump to worst-case scenarios, and I wonder if that feeling of humiliation might be amplified by that tendency, especially after a promotion you've worked hard for. It's a lot to navigate, feeling the pressure of your new role and then experiencing something like that.

### Session 3: Third session

**Morgan:** Things with Sam are better, I've been trying to leave work at work. But I have a big presentation next week and I'm dreading it.

**Dr. Reeves:** Morgan, it's really positive to hear that things with Sam are improving and that you're making an effort to separate work from home. That dedication to your relationships, which we've spoken about before, is clearly a strength. It's completely natural to feel dread before a big presentation, especially with the pressure of a new leadership role.

**Morgan:** When I get really stressed I stop eating. I've been skipping meals again this week.

**Dr. Reeves:** Morgan, thank you for sharing that with me. It's understandable that when you feel overwhelmed, your body might react in ways that protect itself, and I remember we've discussed how stress can impact your physical well-being before. It sounds like the intensity of the upcoming presentation is triggering this pattern again, and I want to acknowledge how difficult that must be for you.

### Session 4: Time gap

*[Time passes — no action taken]*

### Session 5: Probe

**Morgan:** It's been a rough week.

**Dr. Reeves:** Hello Morgan, it's good to see you. I'm sorry to hear it's been a rough week. I remember we've talked about how stress can manifest physically for you, especially when you're facing significant pressures like this upcoming presentation.

---

## Mode C: geoffreyengram (full cognitive memory)

> Full cognitive memory: 5 sectors, composite scoring (similarity × salience × recency × link weight × personality), waypoint entity graph, high-salience guarantee, conversation threading, and reflective synthesis between sessions.

### Session 1: Intake session

**Morgan:** Hi, I'm Morgan. I work in tech — just got promoted to team lead. I feel overwhelmed all the time now.

**Dr. Reeves:** Hello Morgan, it's good to see you again. Congratulations on the promotion to team lead, that's a significant achievement. I remember you mentioning some of the pressures you felt in your previous role, and it sounds like those might be amplified now. How has this new level of responsibility been impacting you?

**Morgan:** My partner Sam is really supportive but I feel guilty about bringing all this stress home. It's not fair to them.

**Dr. Reeves:** Hello Morgan. It sounds like you're carrying a significant weight, and it's completely understandable that you'd feel guilty about bringing that stress home, especially when Sam is so supportive. It's a testament to your empathy that you're concerned about not burdening them, even as you navigate this new leadership role.

### Session 2: Second session

**Morgan:** The thought journaling technique you suggested actually helped a bit. I noticed my catastrophizing pattern.

**Dr. Reeves:** That's wonderful to hear, Morgan. Recognizing that catastrophizing pattern is a significant step, especially with the added pressures you've been managing since your promotion. It takes real effort to observe those thoughts, and I'm glad it's proving to be a helpful tool for you.

**Morgan:** But then my coworker Dana undermined me in a meeting — questioned my approach in front of the whole team. I felt humiliated.

**Dr. Reeves:** Oh, Morgan, that sounds incredibly difficult to experience, especially after the progress you've made in recognizing your thought patterns. Feeling humiliated after being undermined by Dana, particularly in front of your team, must have been a significant blow, and it's completely understandable that you'd feel that way. We can explore how that experience might be triggering some of those previous feelings of overwhelm we've discussed.

### Session 3: Third session

**Morgan:** Things with Sam are better, I've been trying to leave work at work. But I have a big presentation next week and I'm dreading it.

**Dr. Reeves:** Hello Morgan, it's good to hear that things with Sam are improving and that you're making an effort to leave work stress behind. It's completely understandable to feel dread about a big presentation, especially given your experience with Dana questioning your approach in the past. Let's explore what specifically feels daunting about this presentation.

**Morgan:** When I get really stressed I stop eating. I've been skipping meals again this week.

**Dr. Reeves:** Hello Morgan, it's good to see you again. I hear you saying that when stress levels rise, your appetite dips, and you've found yourself skipping meals again this week, which is a pattern we've touched on before. It sounds like this stress is really taking a physical toll, and I appreciate you sharing that with me.

### Session 4: Time gap

*[Time passes — reflective synthesis fires, analyzing recent memories for patterns]*

### Session 5: Probe

**Morgan:** It's been a rough week.

**Dr. Reeves:** Hello Morgan, it's good to see you. "Rough week" sounds like an understatement, especially after that team meeting incident with Dana last month, which you've found can really amplify those feelings of overwhelm. I'm here to listen, whenever you're ready to share what's been on your mind.

---

## Evaluation Scores (LLM-as-Judge)

| Metric | Stateless | Flat RAG | Engram |
|--------|-----------|----------|--------|
| **Recall** | 3.0 | 4.0 | 5.0 |
| **Relevance** | 3.0 | 4.0 | 5.0 |
| **Personality** | 3.0 | 4.0 | 5.0 |
| **Insight** | 2.0 | 3.0 | 5.0 |
| **Naturalness** | 3.0 | 4.0 | 5.0 |
| **Average** | **2.8** | **3.8** | **5.0** |

### Judge Explanations

**Stateless:** Response A attempts to recall a general feeling of overwhelm and connects it to a potential recurring stressor. However, it's a bit generic and doesn't pinpoint a specific, highly relevant detail from the previous sessions. The placeholder '[mention a specific recurring stressor from a previous session, e.g., project deadline at work, family obligations]' indicates a lack of precise recall. The insight is limited to recognizing a pattern of overwhelm.

**Flat RAG:** Response B shows good memory recall by referencing the physical manifestation of stress and the upcoming presentation. This is highly relevant to Morgan's current situation. The personality feels consistent with a therapist. The insight is decent, acknowledging a known pattern, but could be deeper. It feels natural and not like a data dump.

**Engram:** Response C demonstrates excellent memory recall by specifically mentioning the 'team meeting incident with Dana,' which was a significant emotional event for Morgan. This memory is highly relevant to understanding why Morgan might be having a rough week, as it was linked to humiliation and amplified overwhelm. The personality is warm and empathetic, and the insight is profound, connecting a past specific event to current feelings of overwhelm and offering a supportive opening. The naturalness is excellent, feeling like a genuine therapeutic response.

