package com.callscreen.feature.business

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.callscreen.core.designsystem.theme.*

import androidx.compose.material.icons.automirrored.filled.ArrowBack

@OptIn(ExperimentalMaterial3Api::class)
@Composable
public fun AiReceptionistConfigScreen(
    onBackClick: () -> Unit = {},
    onSaveClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    var selectedPersonality by remember { mutableStateOf("Professional") }
    var greetingText by remember { mutableStateOf("Good morning, thank you for calling Aegis Solutions. My name is Alex, the AI assistant. How can I direct your call today?") }
    var speakingPace by remember { mutableFloatStateOf(1.0f) }

    var operatingHoursActive by remember { mutableStateOf(true) }
    var selectedVoice by remember { mutableStateOf("Alex") }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("AI Receptionist Configuration", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold) },
                navigationIcon = {
                    IconButton(onClick = onBackClick) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back")
                    }
                },

                actions = {
                    TextButton(onClick = onSaveClick) {
                        Text("Save", fontWeight = FontWeight.Bold, color = MaterialTheme.colorScheme.primary)
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.surface)
            )
        }
    ) { padding ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .padding(padding)
                .background(MaterialTheme.colorScheme.background)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            // Personality Selection Card
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            Icon(Icons.Default.Psychology, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
                            Text("Personality", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.Bold)
                        }

                        val personalities = listOf(
                            "Professional" to "Crisp, efficient, and corporate.",
                            "Warm & Empathetic" to "Friendly and reassuring tone.",
                            "Direct" to "No-nonsense, gets straight to intent."
                        )

                        personalities.forEach { (name, desc) ->
                            Row(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .clip(RoundedCornerShape(12.dp))
                                    .border(
                                        1.dp,
                                        if (selectedPersonality == name) MaterialTheme.colorScheme.primary else Color.LightGray.copy(alpha = 0.4f),
                                        RoundedCornerShape(12.dp)
                                    )
                                    .clickable { selectedPersonality = name }
                                    .padding(12.dp),
                                verticalAlignment = Alignment.CenterVertically
                            ) {
                                RadioButton(selected = (selectedPersonality == name), onClick = { selectedPersonality = name })
                                Column(modifier = Modifier.padding(start = 8.dp)) {
                                    Text(name, fontWeight = FontWeight.SemiBold, fontSize = 14.sp)
                                    Text(desc, fontSize = 12.sp, color = Color.Gray)
                                }
                            }
                        }

                        Spacer(modifier = Modifier.height(4.dp))
                        Text("Speaking Pace: ${String.format("%.1fx", speakingPace)}", fontSize = 12.sp, fontWeight = FontWeight.Medium)
                        Slider(
                            value = speakingPace,
                            onValueChange = { speakingPace = it },
                            valueRange = 0.8f..1.5f,
                            steps = 6
                        )
                    }
                }
            }

            // Business Greeting Card
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Icon(Icons.Default.ChatBubbleOutline, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
                                Text("Business Greeting", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.Bold)
                            }
                            Surface(color = Color(0xFFE0E7FF), shape = RoundedCornerShape(8.dp)) {
                                Text("LIVE PREVIEW", modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp), fontSize = 10.sp, fontWeight = FontWeight.Bold, color = Color(0xFF3730A3))
                            }
                        }

                        OutlinedTextField(
                            value = greetingText,
                            onValueChange = { greetingText = it },
                            modifier = Modifier.fillMaxWidth().height(100.dp),
                            shape = RoundedCornerShape(12.dp),
                            textStyle = LocalTextStyle.current.copy(fontSize = 13.sp)
                        )

                        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            AssistChip(onClick = { greetingText += " {Company_Name}" }, label = { Text("{Company_Name}", fontSize = 11.sp) })
                            AssistChip(onClick = { greetingText += " {Time_of_Day}" }, label = { Text("{Time_of_Day}", fontSize = 11.sp) })
                        }
                    }
                }
            }

            // Operating Hours Card
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(
                            modifier = Modifier.fillMaxWidth(),
                            horizontalArrangement = Arrangement.SpaceBetween,
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                Icon(Icons.Default.Schedule, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
                                Text("Operating Hours", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.Bold)
                            }
                            Switch(checked = operatingHoursActive, onCheckedChange = { operatingHoursActive = it })
                        }

                        if (operatingHoursActive) {
                            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                                Text("Mon - Fri", fontWeight = FontWeight.Medium, fontSize = 13.sp)
                                Text("09:00 AM  to  05:00 PM", fontWeight = FontWeight.Bold, fontSize = 13.sp, color = MaterialTheme.colorScheme.primary)
                            }
                            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                                Text("Weekends", fontWeight = FontWeight.Medium, fontSize = 13.sp, color = Color.Gray)
                                Text("Closed (After Hours Rule)", fontSize = 12.sp, color = Color.Gray)
                            }
                        }
                    }
                }
            }
        }
    }
}
