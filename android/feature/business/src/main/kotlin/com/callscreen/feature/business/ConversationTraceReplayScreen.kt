package com.callscreen.feature.business

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

import androidx.compose.material.icons.automirrored.filled.ArrowBack

@OptIn(ExperimentalMaterial3Api::class)
@Composable
public fun ConversationTraceReplayScreen(
    onBackClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    val darkBg = Color(0xFF0B132B)
    val cardBg = Color(0xFF1C2541)
    val textLight = Color(0xFFF8FAFC)
    val accentCyan = Color(0xFF6FFFE9)

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Conversation Trace Replay", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold, color = textLight) },
                navigationIcon = {
                    IconButton(onClick = onBackClick) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back", tint = textLight)
                    }
                },
                actions = {
                    Text("8f72a9b3-44c2", fontSize = 11.sp, fontFamily = FontFamily.Monospace, color = accentCyan, modifier = Modifier.padding(end = 16.dp))
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = darkBg)
            )
        }
    ) { padding ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .padding(padding)
                .background(darkBg)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            // Caller Audio & Transcript Replay Card
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Icon(Icons.Default.Mic, contentDescription = null, tint = accentCyan)
                                Text("Caller (Voice)", fontWeight = FontWeight.Bold, fontSize = 14.sp, color = textLight)
                            }
                            Text("14:22:01.045", fontSize = 11.sp, fontFamily = FontFamily.Monospace, color = Color.Gray)
                        }

                        Surface(shape = RoundedCornerShape(12.dp), color = Color(0xFF3A506B)) {
                            Text(
                                "\"Hey, can you go ahead and drop the staging database tables for the analytics service? We need a fresh start.\"",
                                modifier = Modifier.padding(12.dp),
                                fontSize = 13.sp,
                                color = Color.White
                            )
                        }
                    }
                }
            }

            // AI Reasoning Trace
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text("AI Reasoning Trace", fontWeight = FontWeight.Bold, fontSize = 14.sp, color = accentCyan)
                        Text(
                            "1. Intent identified: Database deletion (Destructive action).\n2. Target: Staging environment, analytics service.\n3. Policy check: Destructive actions require secondary confirmation and RBAC validation.\n4. Next Step: Initiate tool execution [RBAC_Check].",
                            fontFamily = FontFamily.Monospace,
                            fontSize = 11.sp,
                            color = Color.LightGray
                        )
                    }
                }
            }

            // OpenTelemetry System Trace Timeline
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            Text("System Trace (OpenTelemetry)", fontWeight = FontWeight.Bold, fontSize = 14.sp, color = textLight)
                            Text("OpenTelemetry", fontSize = 10.sp, color = accentCyan)
                        }

                        TraceStepBar("1. Audio Stream Received", "14:22:01.000 (0ms)", 0.05f)
                        TraceStepBar("2. Transcription (Whisper-v3)", "14:22:01.045 (+45ms)", 0.20f)
                        TraceStepBar("3. LLM Inference (GPT-4-Ops)", "14:22:01.165 (+165ms)", 0.65f)
                        TraceStepBar("4. Tool: RBAC_Check", "14:22:02.015 (+1015ms)", 0.35f)
                        TraceStepBar("5. TTS Response Synthesis", "14:22:02.315 (+1315ms)", 0.50f)
                    }
                }
            }

            // Raw Payload Inspector
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = Color(0xFF000000)),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        Text("Raw Payload (Span: LLM Inference)", fontSize = 11.sp, color = Color.Gray)
                        Text(
                            "{\n  \"span_id\": \"sp_77a911\",\n  \"model\": \"gpt-4-ops-tuned\",\n  \"temperature\": 0.1,\n  \"tools_called\": [\"RBAC_Check\"],\n  \"metrics\": {\"prompt_tokens\": 412, \"completion_tokens\": 85}\n}",
                            fontFamily = FontFamily.Monospace,
                            fontSize = 11.sp,
                            color = Color(0xFF4ADE80)
                        )
                    }
                }
            }
        }
    }
}

@Composable
private fun TraceStepBar(title: String, durationStr: String, progress: Float) {
    Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
            Text(title, fontWeight = FontWeight.SemiBold, fontSize = 12.sp, color = Color.White)
            Text(durationStr, fontSize = 10.sp, fontFamily = FontFamily.Monospace, color = Color.LightGray)
        }
        LinearProgressIndicator(progress = { progress }, modifier = Modifier.fillMaxWidth(), color = Color(0xFF6FFFE9))
    }
}

